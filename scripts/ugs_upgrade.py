#!/usr/bin/env python3
"""Install, upgrade, activate, and roll back a UGS release package."""

import argparse
import copy
import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from contextlib import contextmanager
from pathlib import Path, PurePosixPath


PROFILES = ("baseline", "standard", "high-trust")
KINDS = {"core", "profile-specific", "template", "documentation", "test", "release-only"}
OWNERSHIPS = {"ugs", "project"}


class UpgradeError(Exception):
    """A user-actionable upgrade failure."""


def fail(message):
    print("ugs upgrade: " + message, file=sys.stderr)
    return 1


def read_json(path, description):
    try:
        return json.loads(path.read_text())
    except FileNotFoundError as exc:
        raise UpgradeError(f"{description} does not exist: {path}") from exc
    except (OSError, json.JSONDecodeError) as exc:
        raise UpgradeError(f"{description} is not valid JSON: {path}") from exc


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def sha256_bytes(value):
    return hashlib.sha256(value).hexdigest()


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def is_hex(value, length):
    return (
        isinstance(value, str)
        and len(value) == length
        and all(character in "0123456789abcdef" for character in value)
    )


def safe_relative(value, field):
    if not isinstance(value, str) or not value or "\x00" in value:
        raise UpgradeError(f"invalid {field}: {value!r}")
    path = PurePosixPath(value)
    if (
        "\\" in value
        or path.is_absolute()
        or any(part in ("", ".", "..") for part in path.parts)
        or path.as_posix() != value
    ):
        raise UpgradeError(f"unsafe {field}: {value}")
    return value


def package_files(package_dir):
    files = set()
    for path in package_dir.rglob("*"):
        relative = path.relative_to(package_dir).as_posix()
        if path.is_symlink():
            raise UpgradeError(f"release package contains an unsupported symbolic link: {relative}")
        if path.is_file():
            files.add(relative)
    return files


def validate_component_manifest(package_dir, manifest):
    components_path = package_dir / "COMPONENTS.json"
    components = read_json(components_path, "component manifest")
    if components.get("format") != "ugs-components/v1":
        raise UpgradeError("unsupported component manifest format")
    if components.get("version") != manifest.get("version"):
        raise UpgradeError("component manifest version differs from package manifest")
    if components.get("source_commit") != manifest.get("source_commit"):
        raise UpgradeError("component manifest source commit differs from package manifest")
    entries = components.get("files")
    if not isinstance(entries, list) or not entries:
        raise UpgradeError("component manifest files must be a non-empty array")
    if components.get("active_profile") != "preserved-until-explicit-activation":
        raise UpgradeError("component manifest has an invalid active_profile declaration")

    sources = set()
    targets = set()
    for entry in entries:
        if not isinstance(entry, dict):
            raise UpgradeError("component manifest entries must be objects")
        source = safe_relative(entry.get("source"), "component source")
        if source in sources:
            raise UpgradeError(f"component manifest repeats source: {source}")
        sources.add(source)
        source_path = package_dir / source
        if not source_path.is_file() or source_path.is_symlink():
            raise UpgradeError(f"component source does not exist: {source}")
        component = entry.get("component")
        if not isinstance(component, str) or not component:
            raise UpgradeError(f"invalid component name for {source}")
        kind = entry.get("kind")
        if kind not in KINDS:
            raise UpgradeError(f"invalid component kind for {source}: {kind}")
        ownership = entry.get("ownership")
        if ownership not in OWNERSHIPS:
            raise UpgradeError(f"invalid component ownership for {source}: {ownership}")
        profiles = entry.get("profiles")
        if not isinstance(profiles, list) or not profiles:
            raise UpgradeError(f"invalid component profiles for {source}")
        if any(not isinstance(profile, str) or profile not in PROFILES for profile in profiles):
            raise UpgradeError(f"invalid component profiles for {source}")
        if len(profiles) != len(set(profiles)):
            raise UpgradeError(f"invalid component profiles for {source}")
        target = entry.get("target")
        if target is not None:
            target = safe_relative(target, "component target")
            if target in targets:
                raise UpgradeError(f"component manifest repeats target: {target}")
            targets.add(target)
        elif kind != "release-only":
            raise UpgradeError(f"non-release component has no target: {source}")
        elif ownership != "ugs":
            raise UpgradeError(f"release-only component must be UGS-owned: {source}")
        if entry.get("mode") not in ("0644", "0755"):
            raise UpgradeError(f"invalid component mode for {source}")
        actual_mode = "0755" if source_path.stat().st_mode & stat.S_IXUSR else "0644"
        if entry["mode"] != actual_mode:
            raise UpgradeError(f"component mode does not match source for {source}")

    actual_files = package_files(package_dir)
    manifest_files = set()
    for item in manifest.get("files", []):
        if not isinstance(item, dict):
            raise UpgradeError("package manifest entries must be objects")
        manifest_files.add(safe_relative(item.get("path"), "manifest path"))
    expected_sources = actual_files
    if sources != expected_sources:
        missing = sorted(expected_sources - sources)
        extra = sorted(sources - expected_sources)
        detail = []
        if missing:
            detail.append("unclassified=" + ",".join(missing))
        if extra:
            detail.append("missing-from-package=" + ",".join(extra))
        raise UpgradeError("component manifest does not classify every package file (" + "; ".join(detail) + ")")
    if manifest_files != actual_files - {"MANIFEST.json"}:
        missing = sorted((actual_files - {"MANIFEST.json"}) - manifest_files)
        extra = sorted(manifest_files - actual_files)
        detail = []
        if missing:
            detail.append("manifest-missing=" + ",".join(missing))
        if extra:
            detail.append("manifest-extra=" + ",".join(extra))
        raise UpgradeError("package manifest does not cover the package payload (" + "; ".join(detail) + ")")
    for required in ("MANIFEST.json", "COMPONENTS.json"):
        matches = [item for item in entries if item["source"] == required]
        if len(matches) != 1 or matches[0]["kind"] != "release-only" or matches[0]["target"] is not None:
            raise UpgradeError(f"component manifest must classify {required} as release-only")
    return components


def validate_package(package_dir, manifest_override=None, components_override=None):
    package_dir = package_dir.resolve()
    if not package_dir.is_dir():
        raise UpgradeError(f"package directory does not exist: {package_dir}")
    manifest_path = package_dir / "MANIFEST.json"
    manifest = read_json(manifest_path, "embedded package manifest")
    if manifest.get("format") != "ugs-bootstrap/v1":
        raise UpgradeError("unsupported bootstrap package format")
    if not isinstance(manifest.get("version"), str) or not manifest["version"]:
        raise UpgradeError("package manifest has no version")
    if not is_hex(manifest.get("source_commit"), 40):
        raise UpgradeError("package manifest has an invalid source commit")
    if manifest.get("profiles") != list(PROFILES):
        raise UpgradeError("package manifest has an invalid profile list")
    entries = manifest.get("files")
    if not isinstance(entries, list) or not entries:
        raise UpgradeError("package manifest files must be a non-empty array")
    seen = set()
    for entry in entries:
        if not isinstance(entry, dict):
            raise UpgradeError("package manifest entries must be objects")
        relative = safe_relative(entry.get("path"), "manifest path")
        if relative in seen:
            raise UpgradeError(f"package manifest repeats path: {relative}")
        seen.add(relative)
        expected = entry.get("sha256")
        if not is_hex(expected, 64):
            raise UpgradeError(f"invalid package digest for {relative}")
        path = package_dir / relative
        if not path.is_file() or path.is_symlink():
            raise UpgradeError(f"manifest file is missing: {relative}")
        actual = sha256_file(path)
        if actual != expected:
            raise UpgradeError(f"manifest digest mismatch: {relative}")
    if manifest_override is not None:
        external = read_json(manifest_override.resolve(), "external package manifest")
        if external != manifest:
            raise UpgradeError("external package manifest differs from embedded MANIFEST.json")
    if components_override is not None:
        external = read_json(components_override.resolve(), "external component manifest")
        embedded = read_json(package_dir / "COMPONENTS.json", "component manifest")
        if external != embedded:
            raise UpgradeError("external component manifest differs from embedded COMPONENTS.json")
    components = validate_component_manifest(package_dir, manifest)
    return {"root": package_dir, "manifest": manifest, "components": components}


def parse_checksum(path, archive):
    try:
        lines = [line.strip() for line in path.read_text().splitlines() if line.strip()]
    except OSError as exc:
        raise UpgradeError(f"cannot read checksum file: {path}") from exc
    if len(lines) != 1:
        raise UpgradeError(f"checksum file must contain exactly one entry: {path}")
    parts = lines[0].split()
    if len(parts) < 2 or len(parts[0]) != 64:
        raise UpgradeError(f"invalid checksum entry: {path}")
    listed_name = parts[-1].lstrip("*")
    if Path(listed_name).name != archive.name:
        raise UpgradeError(f"checksum file names {listed_name}, expected {archive.name}")
    return parts[0]


def safe_extract(archive, destination):
    try:
        with tarfile.open(archive, "r:gz") as handle:
            members = handle.getmembers()
            if not members:
                raise UpgradeError("release archive is empty")
            seen_names = set()
            top_levels = set()
            for member in members:
                relative = PurePosixPath(member.name)
                normalized = relative.as_posix()
                if (
                    not member.name
                    or chr(92) in member.name
                    or normalized in seen_names
                    or relative.is_absolute()
                    or any(part in ("", ".", "..") for part in relative.parts)
                ):
                    raise UpgradeError(f"release archive contains an unsafe path: {member.name}")
                seen_names.add(normalized)
                top_levels.add(relative.parts[0])
                if member.issym() or member.islnk():
                    raise UpgradeError(f"release archive contains an unsupported link: {member.name}")
                if not member.isdir() and not member.isfile():
                    raise UpgradeError(f"release archive contains an unsupported special file: {member.name}")
                target = (destination / member.name).resolve()
                if os.path.commonpath([str(destination.resolve()), str(target)]) != str(destination.resolve()):
                    raise UpgradeError(f"release archive escapes extraction directory: {member.name}")
            if len(top_levels) != 1:
                raise UpgradeError("release archive must contain exactly one package directory")
            handle.extractall(destination)
    except (OSError, tarfile.TarError) as exc:
        raise UpgradeError(f"cannot extract release archive: {archive}") from exc


@contextmanager
def package_context(args):
    if args.archive is None:
        yield validate_package(args.package_dir, args.manifest, args.components)
        return

    archive = args.archive.resolve()
    if not archive.is_file():
        raise UpgradeError(f"release archive does not exist: {archive}")
    checksum_path = args.checksum.resolve() if args.checksum else Path(str(archive) + ".sha256")
    if not checksum_path.is_file():
        raise UpgradeError(f"release checksum file is required: {checksum_path}")
    expected = parse_checksum(checksum_path, archive)
    actual = sha256_file(archive)
    if actual != expected:
        raise UpgradeError("release archive checksum does not match its .sha256 file")

    manifest_path = args.manifest.resolve() if args.manifest else Path(str(archive) + ".manifest.json")
    components_path = args.components.resolve() if args.components else Path(str(archive) + ".components.json")
    if not manifest_path.is_file():
        raise UpgradeError(f"external package manifest is required: {manifest_path}")
    if not components_path.is_file():
        raise UpgradeError(f"external component manifest is required: {components_path}")
    with tempfile.TemporaryDirectory(prefix="ugs-upgrade-package-") as temporary:
        extracted = Path(temporary)
        safe_extract(archive, extracted)
        roots = list(extracted.iterdir())
        if len(roots) != 1 or not roots[0].is_dir() or roots[0].is_symlink():
            raise UpgradeError("release archive must contain exactly one package directory")
        package = validate_package(roots[0], manifest_path, components_path)
        yield package


def parse_gitdir(descriptor):
    try:
        content = descriptor.read_text().strip()
    except OSError as exc:
        raise UpgradeError(f"cannot read Git metadata descriptor: {descriptor}") from exc
    if not content.startswith("gitdir:"):
        raise UpgradeError(f"Git metadata descriptor is not a gitdir file: {descriptor}")
    value = content[len("gitdir:"):].strip()
    if not value:
        raise UpgradeError(f"Git metadata descriptor has an empty gitdir: {descriptor}")
    path = Path(value)
    if not path.is_absolute():
        path = descriptor.parent / path
    return path.resolve()


def git_environment(target, managed_git_dir=None):
    environment = os.environ.copy()
    if managed_git_dir is None:
        environment.pop("GIT_DIR", None)
        environment.pop("GIT_WORK_TREE", None)
    else:
        environment["GIT_DIR"] = str(managed_git_dir)
        environment["GIT_WORK_TREE"] = str(target)
    return environment


def run_git(target, args, managed_git_dir=None, check=False):
    result = subprocess.run(
        ["git", "-C", str(target), *args],
        env=git_environment(target, managed_git_dir),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if check and result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"status {result.returncode}"
        raise UpgradeError("Git command failed: " + detail)
    return result


def detect_git_layout(target):
    if not target.is_dir():
        raise UpgradeError(f"target repository directory does not exist: {target}")
    dot_git = target / ".git"
    managed_descriptor = target / ".git-worktree"
    managed_git_dir = None
    layout = None
    if managed_descriptor.exists():
        managed_git_dir = managed_descriptor if managed_descriptor.is_dir() else parse_gitdir(managed_descriptor)
        layout = "managed-worktree"
    elif dot_git.is_dir():
        layout = "normal"
    elif dot_git.is_file():
        layout = "linked-worktree"
    else:
        bare_check = subprocess.run(
            ["git", "--git-dir", str(target), "rev-parse", "--is-bare-repository"],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        if bare_check.returncode == 0 and bare_check.stdout.strip() == "true":
            raise UpgradeError("detected bare Git repository; install UGS into a worktree checkout, not the bare object store")
        raise UpgradeError("target is not a supported Git worktree (expected .git or .git-worktree)")

    result = run_git(target, ["rev-parse", "--is-bare-repository"], managed_git_dir)
    if result.returncode != 0 and managed_git_dir is None and dot_git.is_file():
        managed_git_dir = parse_gitdir(dot_git)
        result = run_git(target, ["rev-parse", "--is-bare-repository"], managed_git_dir)
    if result.returncode != 0:
        detail = result.stderr.strip() or "Git metadata could not be opened"
        raise UpgradeError(f"cannot inspect {layout} Git layout: {detail}")
    if result.stdout.strip() == "true":
        raise UpgradeError("bare Git repositories have no worktree for UGS component installation; use a worktree checkout")
    return {"layout": layout, "managed_git_dir": managed_git_dir}


def git_config(target, context, key):
    result = run_git(target, ["config", "--get", key], context["managed_git_dir"])
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def active_profile(target):
    policy_path = target / ".ugs/policy.json"
    if not policy_path.is_file():
        return "unconfigured"
    policy = read_json(policy_path, "target policy")
    profile = policy.get("conformance_level")
    if profile not in PROFILES:
        raise UpgradeError(f"target policy has an unsupported conformance_level: {profile!r}")
    return profile


def component_entries(package):
    return [entry for entry in package["components"]["files"] if entry.get("target") is not None]


def destination_path(target, relative):
    safe_relative(relative, "destination")
    destination = target.joinpath(*PurePosixPath(relative).parts)
    current = destination.parent
    while current != target and current != current.parent:
        if current.is_symlink():
            raise UpgradeError(f"refusing to write through a symbolic-link directory: {current}")
        if current.exists() and not current.is_dir():
            raise UpgradeError(f"refusing to write through a non-directory path: {current}")
        current = current.parent
    return destination


def make_operation(target, package_root, entry, content=None, relative_override=None, ownership=None, kind=None, component=None):
    relative = relative_override or entry["target"]
    destination = destination_path(target, relative)
    if content is None:
        source = package_root / entry["source"]
        content = source.read_bytes()
        mode = int(entry["mode"], 8)
    else:
        source = None
        mode = 0o644
    exists = destination.exists() or destination.is_symlink()
    if destination.is_symlink() or (exists and not destination.is_file()):
        status = "conflict"
        same = False
    elif exists:
        existing = destination.read_bytes()
        same = existing == content
        mode_same = stat.S_IMODE(os.stat(destination).st_mode) == mode
        status = "unchanged" if same and mode_same else "update"
    else:
        same = False
        status = "add"
    effective_ownership = ownership or entry.get("ownership", "ugs")
    if status == "update" and effective_ownership == "project":
        status = "project-preserved"
    return {
        "relative": relative,
        "destination": destination,
        "content": content,
        "mode": mode,
        "source": source,
        "status": status,
        "same": same,
        "mode_changed": exists and not destination.is_symlink() and stat.S_IMODE(os.stat(destination).st_mode) != mode,
        "ownership": effective_ownership,
        "kind": kind or entry.get("kind", "core"),
        "component": component or entry.get("component", "core"),
    }


def metadata_content(target, package, layout, profile):
    manifest = package["manifest"]
    components = sorted({entry["component"] for entry in component_entries(package)})
    installation = {
        "format": "ugs-installation/v1",
        "package_version": manifest["version"],
        "source_commit": manifest["source_commit"],
        "active_profile": profile,
        "profile_activation": "separate",
        "layout": layout,
        "installed_components": components,
    }
    bootstrap_path = target / ".ugs/bootstrap.json"
    bootstrap = {}
    if bootstrap_path.is_file():
        try:
            bootstrap = json.loads(bootstrap_path.read_text())
        except (OSError, json.JSONDecodeError):
            bootstrap = {}
    bootstrap.update({
        "format": "ugs-bootstrap/v1",
        "version": manifest["version"],
        "profile": profile,
        "active_profile": profile,
        "source_commit": manifest["source_commit"],
        "installed_components": components,
        "profile_activation": "separate",
    })
    return (
        json.dumps(installation, indent=2, sort_keys=True).encode() + b"\n",
        json.dumps(bootstrap, indent=2, sort_keys=True).encode() + b"\n",
    )


def upgrade_plan(target, package, context, command, overwrite_project_files):
    profile = active_profile(target)
    if command == "upgrade" and profile == "unconfigured":
        raise UpgradeError("target has no .ugs/policy.json; initialize UGS first or use the install command")
    if command == "install" and profile == "unconfigured":
        profile = "baseline"
    entries = component_entries(package)
    operations = []
    if command == "install" and not (target / ".ugs/policy.json").exists():
        operations.append(make_operation(
            target,
            package["root"],
            {"source": ".ugs/templates/policy.json", "target": ".ugs/policy.json", "mode": "0644", "ownership": "ugs", "kind": "core", "component": "core"},
        ))
    for entry in entries:
        operations.append(make_operation(target, package["root"], entry))
    installation, bootstrap = metadata_content(target, package, context["layout"], profile)
    operations.append(make_operation(
        target,
        package["root"],
        {"target": ".ugs/installation.json", "ownership": "ugs", "kind": "core", "component": "core"},
        content=installation,
        relative_override=".ugs/installation.json",
    ))
    operations.append(make_operation(
        target,
        package["root"],
        {"target": ".ugs/bootstrap.json", "ownership": "ugs", "kind": "core", "component": "core"},
        content=bootstrap,
        relative_override=".ugs/bootstrap.json",
    ))
    return operations, profile


def activation_policy(current, template, profile):
    updated = copy.deepcopy(current)
    updated["conformance_level"] = profile
    updated.setdefault("commits", {})["signing_level"] = template["commits"]["signing_level"]
    updated.setdefault("review", {})["conclusion_storage"] = template["review"]["conclusion_storage"]
    for section in ("quality", "supply_chain", "repository_shape"):
        if section in template:
            updated[section] = copy.deepcopy(template[section])
        else:
            updated.pop(section, None)
    return updated


def activation_plan(target, package, context, profile):
    policy_path = target / ".ugs/policy.json"
    if not policy_path.is_file():
        raise UpgradeError("profile activation requires an existing .ugs/policy.json")
    current = read_json(policy_path, "target policy")
    required = [
        entry for entry in component_entries(package)
        if entry.get("kind") == "profile-specific" and profile in entry.get("profiles", [])
    ]
    if profile == "high-trust":
        required += [
            entry for entry in component_entries(package)
            if entry.get("kind") == "profile-specific" and "standard" in entry.get("profiles", [])
            and entry not in required
        ]
    missing = [entry["target"] for entry in required if not (target / entry["target"]).is_file()]
    if missing:
        raise UpgradeError("profile components are not installed; run `ugs upgrade` first: " + ", ".join(sorted(missing)))
    template_name = {
        "baseline": ".ugs/templates/policy.json",
        "standard": ".ugs/templates/policy-standard.json",
        "high-trust": ".ugs/templates/policy-high-trust.json",
    }[profile]
    template = read_json(package["root"] / template_name, "profile template")
    updated = json.dumps(activation_policy(current, template, profile), indent=2, sort_keys=True).encode() + b"\n"
    installation, bootstrap = metadata_content(target, package, context["layout"], profile)
    operations = [
        make_operation(
            target,
            package["root"],
            {"target": ".ugs/policy.json", "ownership": "ugs", "kind": "core", "component": "core"},
            content=updated,
            relative_override=".ugs/policy.json",
        ),
        make_operation(
            target,
            package["root"],
            {"target": ".ugs/installation.json", "ownership": "ugs", "kind": "core", "component": "core"},
            content=installation,
            relative_override=".ugs/installation.json",
        ),
        make_operation(
            target,
            package["root"],
            {"target": ".ugs/bootstrap.json", "ownership": "ugs", "kind": "core", "component": "core"},
            content=bootstrap,
            relative_override=".ugs/bootstrap.json",
        ),
    ]
    return operations, current.get("conformance_level", "unknown"), profile


def print_plan(operations, layout, profile, command, hooks_changed=False):
    print(f"repository layout: {layout}")
    print(f"active profile: {profile}")
    print(f"operation: {command}")
    for operation in operations:
        label = operation["status"]
        print(f"{label}: {operation['relative']} [{operation['component']}/{operation['kind']}]")
    if hooks_changed:
        print("update: git config core.hooksPath .githooks [core]")


def backup_path(target, requested):
    if requested is not None:
        path = requested.resolve()
        target_resolved = target.resolve()
        if requested.is_symlink():
            raise UpgradeError("backup directory must not be a symbolic link")
        try:
            path.relative_to(target_resolved)
        except ValueError:
            pass
        else:
            raise UpgradeError("backup directory must be outside the target repository")
        if path.exists() and any(path.iterdir()):
            raise UpgradeError(f"backup directory is not empty: {path}")
        path.mkdir(parents=True, exist_ok=True)
        return path
    return Path(tempfile.mkdtemp(prefix=".ugs-backup-", dir=str(target.parent)))


def create_backup(target, operations, hooks_before, requested):
    path = backup_path(target, requested)
    file_records = []
    for operation in operations:
        destination = operation["destination"]
        record = {
            "path": operation["relative"],
            "existed": destination.exists(),
            "before_sha256": sha256_file(destination) if destination.is_file() else None,
            "before_mode": format(os.stat(destination).st_mode & 0o777, "04o") if destination.is_file() else None,
            "after_sha256": sha256_bytes(operation["content"]),
            "after_mode": format(operation["mode"], "04o"),
            "mode": operation["mode"],
        }
        if destination.exists() and destination.is_file():
            backup_file = path / "files" / PurePosixPath(operation["relative"])
            backup_file.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(destination, backup_file)
            record["backup_path"] = backup_file.relative_to(path).as_posix()
        file_records.append(record)
    metadata = {
        "format": "ugs-backup/v1",
        "target": str(target.resolve()),
        "files": file_records,
        "core.hooksPath": hooks_before,
    }
    write_json(path / "BACKUP.json", metadata)
    return path


def restore_backup(backup, target, force=False):
    metadata = read_json(backup / "BACKUP.json", "backup metadata")
    if metadata.get("format") != "ugs-backup/v1":
        raise UpgradeError("unsupported backup format")
    if not isinstance(metadata.get("target"), str) or not metadata["target"]:
        raise UpgradeError("backup metadata has no target")
    if not isinstance(metadata.get("files"), list):
        raise UpgradeError("backup metadata files must be an array")
    context = detect_git_layout(target)
    recorded_target = Path(metadata["target"]).resolve()
    if recorded_target != target.resolve():
        raise UpgradeError(f"backup belongs to {recorded_target}, not {target.resolve()}")
    conflicts = []
    records = []
    seen_paths = set()
    for record in metadata.get("files", []):
        if not isinstance(record, dict):
            raise UpgradeError("backup metadata entries must be objects")
        relative = safe_relative(record.get("path"), "backup path")
        if relative in seen_paths:
            raise UpgradeError(f"backup metadata repeats path: {relative}")
        seen_paths.add(relative)
        destination = destination_path(target, relative)
        current_digest = sha256_file(destination) if destination.is_file() else None
        current_mode = (
            format(stat.S_IMODE(os.stat(destination).st_mode), "04o")
            if destination.is_file()
            else None
        )
        expected_after_mode = record.get("after_mode")
        if not force and (
            current_digest != record.get("after_sha256")
            or (expected_after_mode is not None and current_mode != expected_after_mode)
        ):
            conflicts.append(relative)
        existed = record.get("existed")
        if not isinstance(existed, bool):
            raise UpgradeError(f"backup existence flag is invalid: {relative}")
        if existed:
            backup_name = safe_relative(record.get("backup_path"), "backup file")
            backup_file = backup / PurePosixPath(backup_name)
            if not backup_file.is_file() or backup_file.is_symlink():
                raise UpgradeError(f"backup file is missing: {backup_file}")
            before_digest = record.get("before_sha256")
            if before_digest and sha256_file(backup_file) != before_digest:
                raise UpgradeError(f"backup file digest mismatch: {backup_file}")
        elif record.get("backup_path") is not None:
            raise UpgradeError(f"new file record must not have a backup file: {relative}")
        records.append(record)
    if conflicts:
        raise UpgradeError("rollback refused because files changed after installation: " + ", ".join(conflicts) + "; use --force to override")
    stage = Path(tempfile.mkdtemp(prefix=".ugs-rollback-", dir=str(target.parent)))
    try:
        for record in records:
            relative = safe_relative(record["path"], "backup path")
            destination = destination_path(target, relative)
            if record.get("existed"):
                backup_file = backup / safe_relative(record["backup_path"], "backup file")
                staged = stage / PurePosixPath(relative)
                staged.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(backup_file, staged)
                os.chmod(staged, int(record.get("before_mode") or record.get("mode", "0644"), 8))
                destination.parent.mkdir(parents=True, exist_ok=True)
                os.replace(staged, destination)
            elif destination.exists() or destination.is_symlink():
                if destination.is_dir() and not destination.is_symlink():
                    raise UpgradeError(f"rollback target is now a directory: {relative}")
                destination.unlink()
        hooks_before = metadata.get("core.hooksPath")
        if hooks_before is not None and not isinstance(hooks_before, str):
            raise UpgradeError("backup core.hooksPath value is invalid")
        if hooks_before is None:
            run_git(target, ["config", "--unset", "core.hooksPath"], context["managed_git_dir"])
        else:
            run_git(target, ["config", "core.hooksPath", hooks_before], context["managed_git_dir"], check=True)
    finally:
        shutil.rmtree(stage, ignore_errors=True)


def apply_operations(target, operations, context, backup_requested):
    changed = [operation for operation in operations if operation["status"] in ("add", "update")]
    hooks_before = git_config(target, context, "core.hooksPath")
    hooks_changed = hooks_before != ".githooks"
    if not changed and not hooks_changed:
        print("no changes required")
        return None
    backup = create_backup(target, changed, hooks_before, backup_requested)
    stage = Path(tempfile.mkdtemp(prefix=".ugs-upgrade-", dir=str(target.parent)))
    try:
        for operation in changed:
            staged = stage / PurePosixPath(operation["relative"])
            staged.parent.mkdir(parents=True, exist_ok=True)
            staged.write_bytes(operation["content"])
            os.chmod(staged, operation["mode"])
        for operation in changed:
            destination = operation["destination"]
            destination.parent.mkdir(parents=True, exist_ok=True)
            os.replace(stage / PurePosixPath(operation["relative"]), destination)
        if hooks_changed:
            run_git(target, ["config", "core.hooksPath", ".githooks"], context["managed_git_dir"], check=True)
    except Exception:
        try:
            restore_backup(backup, target, force=True)
        except Exception as rollback_error:
            print("ugs upgrade: automatic rollback failed: " + str(rollback_error), file=sys.stderr)
        raise
    finally:
        shutil.rmtree(stage, ignore_errors=True)
    print("backup: " + str(backup))
    print("rollback: scripts/ugs.sh rollback --backup-dir " + str(backup) + " " + str(target))
    return backup


def command_install(args, command):
    target = args.target.resolve()
    with package_context(args) as package:
        context = detect_git_layout(target)
        overwrite_project_files = args.overwrite_project_files or args.overwrite
        operations, profile = upgrade_plan(target, package, context, command, overwrite_project_files)
        if overwrite_project_files:
            for operation in operations:
                if operation["status"] == "project-preserved":
                    operation["status"] = "update"
        conflicts = [operation for operation in operations if operation["status"] == "conflict"]
        hooks_changed = git_config(target, context, "core.hooksPath") != ".githooks"
        print_plan(operations, context["layout"], profile, command, hooks_changed)
        if conflicts:
            if args.dry_run:
                print("conflicts detected; no files were changed", file=sys.stderr)
                return 1
            print("conflicts detected; no files were changed", file=sys.stderr)
            return 1
        if args.dry_run:
            print("dry-run complete; no files were changed")
            return 0
        apply_operations(target, operations, context, args.backup_dir)
        print(f"UGS {command} complete: {target}")
        return 0


def command_activate(args):
    target = args.target.resolve()
    with package_context(args) as package:
        context = detect_git_layout(target)
        operations, previous, profile = activation_plan(target, package, context, args.profile)
        print_plan(operations, context["layout"], previous, "activate -> " + profile)
        conflicts = [operation for operation in operations if operation["status"] == "conflict"]
        if conflicts:
            print("conflicts detected; no files were changed", file=sys.stderr)
            return 1
        if args.dry_run:
            print("dry-run complete; no files were changed")
            return 0
        apply_operations(target, operations, context, args.backup_dir)
        print(f"UGS profile activated: {profile}")
        return 0


def command_rollback(args):
    target = args.target.resolve()
    backup = args.backup_dir.resolve()
    if not backup.is_dir():
        return fail("backup directory does not exist: " + str(backup))
    restore_backup(backup, target, force=args.force)
    print("rollback complete: " + str(target))
    return 0


def add_package_options(parser):
    parser.add_argument("--package-dir", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--archive", type=Path, help="verify and use a .tar.gz release archive")
    parser.add_argument("--manifest", type=Path, help="external .manifest.json to compare with MANIFEST.json")
    parser.add_argument("--components", type=Path, help="external .components.json to compare with COMPONENTS.json")
    parser.add_argument("--checksum", type=Path, help="archive .sha256 file")


def main():
    parser = argparse.ArgumentParser(description="install and safely upgrade a UGS release package")
    subparsers = parser.add_subparsers(dest="command", required=True)

    for name in ("install", "upgrade"):
        command = subparsers.add_parser(name, help=f"{name} all release components without changing the active profile")
        add_package_options(command)
        command.add_argument("--dry-run", action="store_true")
        command.add_argument("--backup-dir", type=Path)
        command.add_argument("--overwrite-project-files", action="store_true")
        command.add_argument("--overwrite", action="store_true", help=argparse.SUPPRESS)
        command.add_argument("target", type=Path)

    activate = subparsers.add_parser("activate", help="explicitly activate a conformance profile")
    add_package_options(activate)
    activate.add_argument("--profile", choices=PROFILES, required=True)
    activate.add_argument("--dry-run", action="store_true")
    activate.add_argument("--backup-dir", type=Path)
    activate.add_argument("target", type=Path)

    rollback = subparsers.add_parser("rollback", help="restore a backup made by install, upgrade, or activate")
    rollback.add_argument("--backup-dir", type=Path, required=True)
    rollback.add_argument("--force", action="store_true", help="restore even when a file changed after the backup")
    rollback.add_argument("target", type=Path)

    args = parser.parse_args()
    try:
        if args.command in ("install", "upgrade"):
            return command_install(args, args.command)
        if args.command == "activate":
            return command_activate(args)
        return command_rollback(args)
    except UpgradeError as exc:
        return fail(str(exc))
    except (OSError, subprocess.SubprocessError) as exc:
        return fail(str(exc))


if __name__ == "__main__":
    sys.exit(main())
