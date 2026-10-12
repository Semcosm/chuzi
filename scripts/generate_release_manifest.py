#!/usr/bin/env python3
"""Generate the UI-neutral manifest shipped with a chuzi nightly package."""
import argparse
import hashlib
import json
import os
import re
from datetime import datetime, timezone
from pathlib import Path

ADAPTER_API = "chuzi.adapter/v1"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def normalise_thumbprint(value: object) -> str | None:
    if not isinstance(value, str):
        return None
    value = value.replace(" ", "").upper()
    return value if re.fullmatch(r"[0-9A-F]{40}", value) else None


def resource(stage: Path, relative: str) -> dict:
    path = stage / relative
    return {"path": relative, "sha256": sha256(path), "size": path.stat().st_size}


def builtin_plugins(stage: Path, version: str) -> list[dict]:
    manifest_path = stage / "browser-worker" / "worker-manifest.json"
    if not manifest_path.is_file():
        raise SystemExit(f"browser worker manifest is missing: {manifest_path}")
    try:
        worker_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SystemExit(f"invalid browser worker manifest: {exc}") from exc
    if worker_manifest.get("protocol") != "v1":
        raise SystemExit("browser worker manifest protocol must be v1")
    adapters = worker_manifest.get("adapters")
    if not isinstance(adapters, list):
        raise SystemExit("browser worker manifest adapters must be a list")

    plugins = []
    seen_ids = set()
    for adapter in adapters:
        if not isinstance(adapter, dict):
            raise SystemExit("browser worker adapter descriptors must be objects")
        adapter_id = adapter.get("id")
        adapter_api = adapter.get("api")
        entry = adapter.get("entry")
        capabilities = adapter.get("capabilities", [])
        if not isinstance(adapter_id, str) or not adapter_id.strip():
            raise SystemExit("browser worker adapter id is required")
        if adapter_id in seen_ids:
            raise SystemExit(f"duplicate browser worker adapter: {adapter_id}")
        if adapter_api != ADAPTER_API:
            raise SystemExit(f"browser worker adapter {adapter_id} has unsupported api: {adapter_api}")
        if not isinstance(entry, str) or not entry or \
                entry.startswith(('/', '\\')) or any(part in ('', '.', '..') for part in entry.replace('\\', '/').split('/')):
            raise SystemExit(f"browser worker adapter {adapter_id} entry path is unsafe")
        entry_path = stage / "browser-worker" / entry
        if not entry_path.is_file():
            raise SystemExit(f"browser worker adapter {adapter_id} entry is missing: {entry}")
        if not isinstance(capabilities, list) or any(not isinstance(item, str) or not item.strip() for item in capabilities):
            raise SystemExit(f"browser worker adapter {adapter_id} capabilities are invalid")
        if len(set(capabilities)) != len(capabilities):
            raise SystemExit(f"browser worker adapter {adapter_id} has duplicate capabilities")
        seen_ids.add(adapter_id)
        # Genshin is shipped as an independent adapter archive in stage 2.
        if adapter_id == "genshin-cloudgame":
            continue
        plugins.append({
            "id": adapter_id,
            "version": version,
            "api": ADAPTER_API,
            "distribution": "builtin",
            "source_component": "browser-worker",
            "capabilities": capabilities,
            "installable": False,
        })
    return plugins


def package_plugin(archive: Path, manifest_path: Path, version: str, target: str) -> dict:
    try:
        descriptor = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SystemExit(f"invalid adapter package manifest: {exc}") from exc
    for key in ("id", "api", "entry", "capabilities", "permissions", "targets", "signed_by"):
        if key not in descriptor:
            raise SystemExit(f"adapter package manifest misses {key}")
    targets = descriptor["targets"]
    if (not isinstance(targets, list) or not targets or
            any(not isinstance(item, str) or not item.strip() for item in targets) or
            len(set(targets)) != len(targets)):
        raise SystemExit("adapter package manifest targets are invalid")
    if target not in targets:
        raise SystemExit(f"adapter package does not support target: {target}")
    if descriptor["api"] != ADAPTER_API or not isinstance(descriptor["signed_by"], str) or not descriptor["signed_by"].strip():
        raise SystemExit("adapter package manifest api or signer is invalid")
    if not isinstance(descriptor["capabilities"], list) or not descriptor["capabilities"]:
        raise SystemExit("adapter package manifest capabilities are invalid")
    if not isinstance(descriptor["permissions"], list):
        raise SystemExit("adapter package manifest permissions are invalid")
    return {
        "id": descriptor["id"],
        "version": version,
        "api": descriptor["api"],
        "entry": descriptor["entry"],
        "distribution": "package",
        "target": target,
        "archive": archive.name,
        "sha256": sha256(archive),
        "capabilities": descriptor["capabilities"],
        "permissions": descriptor["permissions"],
        "signed_by": descriptor.get("signed_by", ""),
        "installable": True,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", required=True, type=Path)
    parser.add_argument("--target", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--channel", default="nightly")
    parser.add_argument("--adapter-archive", type=Path)
    parser.add_argument("--adapter-manifest", type=Path)
    args = parser.parse_args()
    if args.channel not in {"nightly", "test", "stable"}:
        raise SystemExit(f"unsupported release channel: {args.channel}")
    if args.channel == "test" and not re.fullmatch(r"test-[0-9]+-[0-9a-fA-F]{12}", args.version):
        raise SystemExit("test versions must match test-<run>-<sha12>")
    if args.channel == "stable" and not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.version):
        raise SystemExit("stable versions must match v<major>.<minor>.<patch>")
    stage = args.stage.resolve()
    binary = "chuzi.exe" if args.target == "windows-amd64" else "chuzi"
    launcher = "chuzi-launcher.exe" if args.target == "windows-amd64" else "chuzi-launcher"
    browser_launcher = "chuzi-browser-launcher.exe"
    user_agent = "chuzi-user-agent.exe"
    node_runtime = "node.exe"
    archive_extension = "zip" if args.target == "windows-amd64" else "tar.gz"
    service_files = [binary]
    if args.target == "windows-amd64":
        service_files.extend([
            browser_launcher,
            user_agent,
            "chuzi-session-broker.exe",
            node_runtime,
            "session-shell.ps1",
            "windows-service-management.ps1",
            "validate_windows_payload.ps1",
            "authenticode-policy.json",
            "authenticode-signing.json",
        ])
        if args.channel == "stable":
            try:
                signing_policy = json.loads((stage / "authenticode-policy.json").read_text(encoding="utf-8"))
                signing_evidence = json.loads((stage / "authenticode-signing.json").read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                raise SystemExit(f"production Authenticode metadata is missing or invalid: {exc}") from exc
            production_files = signing_policy.get("production_required_files")
            if (not isinstance(production_files, list) or not production_files or
                    len(set(production_files)) != len(production_files) or
                    any(not isinstance(item, str) or not item or "\\" in item or
                        any(part in ("", ".", "..") for part in item.split("/"))
                        for item in production_files)):
                raise SystemExit("production Authenticode file policy is missing")
            missing_production = [item for item in production_files if not (stage / item).is_file()]
            if missing_production:
                raise SystemExit("production Windows helper files are missing: " + ", ".join(missing_production))
            signing_order = ["authenticode", "release-manifest", "archive", "release-index", "attestation"]
            if signing_policy.get("status") != "production-signed" or not signing_policy.get("production_release_allowed"):
                raise SystemExit("production Authenticode signer is not configured")
            if signing_policy.get("artifact_kind") != "payload" or signing_evidence.get("artifact_kind") != "payload":
                raise SystemExit("production Authenticode artifact scope is invalid")
            if signing_policy.get("signing_order") != signing_order or signing_evidence.get("signing_order") != signing_order:
                raise SystemExit("production Authenticode signing order is invalid")
            allowlist = signing_policy.get("allowed_thumbprints")
            normalized_allowlist = ([normalise_thumbprint(item) for item in allowlist]
                                    if isinstance(allowlist, list) else [])
            if (not isinstance(allowlist, list) or not allowlist or
                    any(item is None for item in normalized_allowlist) or
                    len(set(normalized_allowlist)) != len(normalized_allowlist)):
                raise SystemExit("production Authenticode publisher binding is invalid")
            if signing_evidence.get("status") != "production-signed" or not signing_evidence.get("production_release_allowed") or not signing_evidence.get("hashes_computed_after_signing"):
                raise SystemExit("production Authenticode evidence is invalid")
            evidence_allowlist = signing_evidence.get("allowed_thumbprints")
            normalized_evidence_allowlist = ([normalise_thumbprint(item) for item in evidence_allowlist]
                                             if isinstance(evidence_allowlist, list) else [])
            if (signing_evidence.get("publisher_subject_prefix") != signing_policy.get("publisher_subject_prefix") or
                    set(normalized_evidence_allowlist) != set(normalized_allowlist) or
                    any(item is None for item in normalized_evidence_allowlist)):
                raise SystemExit("production Authenticode publisher evidence is invalid")
            evidence_files = signing_evidence.get("files")
            if (not isinstance(evidence_files, list) or
                    {item.get("path") for item in evidence_files if isinstance(item, dict)} != set(production_files) or
                    len(evidence_files) != len(production_files)):
                raise SystemExit("production Authenticode file evidence is incomplete")
            timestamp = signing_evidence.get("timestamp")
            if not isinstance(timestamp, dict) or not timestamp.get("present") or any(not isinstance(item, dict) or not item.get("timestamp_present") for item in evidence_files):
                raise SystemExit("production Authenticode timestamp evidence is missing")
            for record in evidence_files:
                if not isinstance(record, dict) or record.get("path") not in production_files:
                    raise SystemExit("production Authenticode file evidence is invalid")
                record_thumbprint = normalise_thumbprint(record.get("thumbprint"))
                record_hash = record.get("sha256")
                if record_thumbprint not in set(normalized_allowlist) or not isinstance(record_hash, str) or not re.fullmatch(r"[0-9a-fA-F]{64}", record_hash):
                    raise SystemExit("production Authenticode file evidence is invalid")
                if sha256(stage / record["path"]).lower() != record_hash.lower():
                    raise SystemExit("production Authenticode evidence hash mismatch")
    groups = {
        "launcher": ([launcher,], True),
        "service": (service_files, False),
        "browser-worker": (sorted(
            "browser-worker/" + str(path.relative_to(stage / "browser-worker")).replace(os.sep, "/")
            for path in (stage / "browser-worker").rglob("*") if path.is_file()
        ), False),
    }
    if args.target == "windows-amd64":
        groups["presentmon"] = (["PresentMon.exe", "PresentMon-LICENSE.txt"], True)
    components = []
    for component_id, (files, required) in groups.items():
        files = files if isinstance(files, list) else list(files)
        missing = [item for item in files if not (stage / item).is_file()]
        if missing:
            raise SystemExit(
                f"component {component_id} is missing resources: {', '.join(missing)}"
            )
        if required and not files:
            raise SystemExit(f"required component has no resources: {component_id}")
        dependencies = ["browser-worker"] if component_id == "service" else []
        components.append({
            "id": component_id,
            "version": args.version,
            "required": required,
            "dependencies": dependencies,
            "artifact": f"chuzi-{args.version}-{args.target}-{component_id}.{archive_extension}",
            "resources": [resource(stage, item) for item in files],
        })
    plugins = builtin_plugins(stage, args.version)
    if args.adapter_archive or args.adapter_manifest:
        if not args.adapter_archive or not args.adapter_manifest:
            raise SystemExit("adapter archive and manifest must be supplied together")
        plugins.append(package_plugin(args.adapter_archive.resolve(), args.adapter_manifest.resolve(), args.version, args.target))
    manifest = {
        "format": "chuzi-release/v1",
        "channel": args.channel,
        "version": args.version,
        "commit": args.commit,
        "target": args.target,
        "generated_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "components": components,
        "plugins": plugins,
    }
    output = stage / "release-manifest.json"
    output.write_text(json.dumps(manifest, indent=2, sort_keys=False) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
