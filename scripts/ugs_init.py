#!/usr/bin/env python3
"""Generate a UGS baseline in an empty or explicitly migrated repository."""
import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

def error(message):
    print("ugs init: " + message, file=sys.stderr)
    return 1

def git(target, *args):
    return subprocess.run(["git", "-C", str(target), *args], check=True, stdout=subprocess.PIPE, text=True).stdout.strip()

def files(source, profile, with_document_map=False):
    template = source / "bootstrap" / "templates"
    policy_name = {"baseline": "policy.json", "standard": "policy-standard.json", "high-trust": "policy-high-trust.json"}[profile]
    policy = json.loads((template / policy_name).read_text())
    signing_level = {"baseline": "unsigned", "standard": "commits-signed", "high-trust": "high-trust-commits-signed"}[profile]
    output = {
        ".ugs/policy.json": json.dumps(policy, indent=2) + "\n",
        ".ugs/bootstrap.json": "",
        ".ugs/schema/policy.schema.json": ((source / ".ugs/schema/policy.schema.json") if (source / ".ugs/schema/policy.schema.json").exists() else (source / "bootstrap/templates/policy.schema.json")).read_text(),
        ".githooks/README.md": "# UGS managed hooks\n\nInstall with `git config core.hooksPath .githooks`.\n",
        ".githooks/commit-msg": "#!/usr/bin/env bash\nset -euo pipefail\ngrep -Eq '^[a-z]+(\\([^)]+\\))?: .+' \"$1\" || { echo 'UGS: invalid commit subject' >&2; exit 1; }\n",
        "REPOSITORY_POLICY.md": "# Repository Policy\n\nUGS Profile: continuous\nMerge Strategy: rebase-ff\nVersioning: semver\nSigning Level: " + signing_level + "\nProtected Long-Lived Branches: main\nHooks Path: .githooks\n",
        "README.md": "# UGS-governed repository\n\nThis repository was initialized by the UGS bootstrap package.\n\nRun `scripts/validate_policy_manifest.sh` to validate the policy.\n",
        "cr/README.md": "# Change Requests\n\nRecord accepted changes under `cr/` using the UGS CR template.\n\nThe `scripts/create_pr_from_cr.sh` and `scripts/validate_pr_cr.sh` commands are compatibility wrappers for the optional GitHub adapter. In the baseline profile they report that the adapter is not installed; initialize or migrate with `--profile standard` or `--profile high-trust` to enable them.\n",
        "cr/TEMPLATE.md": "# CR-XXXX: <title>\n\nBase: main\nHead or Range: <commit-or-range>\nRevision: 1\nStatus: pending\nDecision: pending\nPolicy Version: v0.3\nBase OID: <base-oid>\nHead OID: <head-oid>\nIntegrated Result: pending\n\n## Summary\n\n<summary>\n\n## Motivation\n\n<motivation>\n\n## Test Evidence\n\n<test evidence>\n\n## Risk\n\n<risk>\n\n## Rollback\n\n<rollback>\n\n## Breaking Change\n\n<breaking change>\n\n## Backport Target\n\n<backport target>\n",
        "scripts/validate_policy_manifest.sh": (source / "scripts/validate_policy_manifest.sh").read_text(),
        "scripts/validate_cr_record.sh": (source / "scripts/validate_cr_record.sh").read_text(),
        "scripts/validate_cr_review.sh": (source / "scripts/validate_cr_review.sh").read_text(),
        "adapters/bare-git/update": (source / "adapters/bare-git/update").read_text(),
        "scripts/validate_pr_cr.sh": (source / "scripts/validate_pr_cr.sh").read_text(),
        "scripts/create_pr_from_cr.sh": (source / "scripts/create_pr_from_cr.sh").read_text(),
        "scripts/validate_main_cr_range.sh": (source / "scripts/validate_main_cr_range.sh").read_text(),
        "scripts/validate_ref_update.sh": (source / "scripts/validate_ref_update.sh").read_text(),
    }
    if with_document_map:
        document_map_schema = source / ".ugs/schema/document-map.schema.json"
        if not document_map_schema.exists():
            document_map_schema = template / "document-map.schema.json"
        output.update({
            ".ugs/document-map.json": (template / "document-map.json").read_text(),
            ".ugs/schema/document-map.schema.json": document_map_schema.read_text(),
            "scripts/generate_document_map.py": (source / "scripts/generate_document_map.py").read_text(),
            "scripts/validate_document_map.py": (source / "scripts/validate_document_map.py").read_text(),
        })
        output["README.md"] += (
            "\n## Document Map（文档映射）\n\n"
            "- [Repository README（仓库 README）](README.md)\n\n"
        )
    if profile in ("standard", "high-trust"):
        output.update({
            "adapters/github/validate_pr.sh": (source / "adapters/github/validate_pr.sh").read_text(),
            "adapters/github/create_pr_from_cr.sh": (source / "adapters/github/create_pr_from_cr.sh").read_text(),
            "adapters/github/validate_adapter.sh": (source / "adapters/github/validate_adapter.sh").read_text(),
            "adapters/github/validate_action_pinning.sh": (source / "adapters/github/validate_action_pinning.sh").read_text(),
            "LICENSE": "# License\n\nThis repository has not selected a license. Replace this file before distributing software.\n",
            "SECURITY.md": "# Security\n\nReport security issues privately to the repository maintainers.\n",
            "CODE_OF_CONDUCT.md": "# Code of Conduct\n\nContributors are expected to act respectfully and in good faith.\n",
            "SUPPORT.md": "# Support\n\nUse the repository issue tracker for support requests.\n",
            "RELEASE.md": "# Release Guide\n\nReleases use signed annotated semantic-version tags and the UGS release workflow.\n",
            ".ugs/supply-chain/README.md": "# Supply-chain evidence\n\nThis standard profile reserves this directory for release SBOMs, build records, and attestations.\n",
            ".github/workflows/ugs-validate.yml": (source / "bootstrap/templates/standard-workflow.yml").read_text(),
        })
        for name in ("validate_quality_profile.sh", "validate_supply_chain_profile.sh",
                     "validate_supply_chain_evidence.sh", "validate_action_pinning.sh",
                     "validate_repository_shape.sh"):
            output["scripts/" + name] = (source / "scripts" / name).read_text()
    if profile == "high-trust":
        for relative in ("keys/README.md", "keys/allowed_signers", "keys/revoked_signers", "keys/signer_roles.json", ".ugs/schema/signer-roles.schema.json"):
            output[relative] = (source / relative).read_text()
        for name in ("validate_signer_roles.sh", "validate_commit_signatures.sh", "validate_release_tag.sh", "validate_release_attestation.sh"):
            output["scripts/" + name] = (source / "scripts" / name).read_text()
        output[".github/workflows/ugs-validate.yml"] = (source / ".github/workflows/ugs-validate.yml").read_text()
    return output

def main():
    parser = argparse.ArgumentParser(description="initialize a UGS-governed repository")
    parser.add_argument("target", nargs="?", default=".")
    parser.add_argument("--profile", choices=["baseline", "standard", "high-trust"], default="baseline")
    parser.add_argument("--name", default="")
    parser.add_argument("--version", default="source")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--migrate", action="store_true", help="allow an existing repository with files")
    parser.add_argument("--no-commit", action="store_true")
    parser.add_argument("--with-document-map", action="store_true", help="enable the optional generated README Document Map")
    args = parser.parse_args()
    target = Path(args.target).resolve()
    source = Path(__file__).resolve().parent.parent
    if not target.exists(): target.mkdir(parents=True)
    if not target.is_dir(): return error("target is not a directory")
    try:
        is_git = (target / ".git").exists()
        has_files = any(target.iterdir())
        if has_files and not args.migrate and not (is_git and not any(p.name != ".git" for p in target.iterdir())):
            return error("target is not empty; use --migrate explicitly")
        if not is_git and not args.dry_run: subprocess.run(["git", "init", "--initial-branch", "main", str(target)], check=True, stdout=subprocess.DEVNULL)
        if not args.dry_run and not args.no_commit:
            identity = subprocess.run(["git", "-C", str(target), "var", "GIT_AUTHOR_IDENT"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if identity.returncode != 0: return error("Git author identity is not configured; use --no-commit or configure user.name and user.email")
        output = files(source, args.profile, args.with_document_map)
        output[".ugs/bootstrap.json"] = json.dumps({"format": "ugs-bootstrap/v1", "version": args.version, "profile": args.profile, "name": args.name or target.name, "document_map": args.with_document_map}, indent=2) + "\n"
        if args.migrate:
            output = {relative: content for relative, content in output.items() if not (target / relative).exists()}
        for relative in output:
            destination = target / relative
            if destination.exists() and not args.migrate: return error("refusing to overwrite " + relative)
            print(("would write " if args.dry_run else "write ") + relative)
        if args.dry_run: return 0
        staging = Path(tempfile.mkdtemp(prefix="ugs-init-", dir=str(target.parent)))
        try:
            for relative, content in output.items():
                destination = staging / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_text(content)
                if relative.endswith(".sh") or relative.endswith(".py") or relative.endswith("commit-msg") or relative.startswith("adapters/"): destination.chmod(0o755)
            for relative in output:
                destination = target / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                os.replace(staging / relative, destination)
        finally: shutil.rmtree(staging, ignore_errors=True)
        subprocess.run(["git", "-C", str(target), "config", "core.hooksPath", ".githooks"], check=True)
        has_head = subprocess.run(["git", "-C", str(target), "rev-parse", "--verify", "HEAD"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
        if not args.no_commit and not has_head:
            subprocess.run(["git", "-C", str(target), "add", "--"] + list(output), check=True)
            commit_command = ["git", "-C", str(target), "commit"]
            if args.profile == "high-trust": commit_command.append("-S")
            subprocess.run(commit_command + ["-m", "chore(bootstrap): initialize UGS governance", "-m", "Refs: ugs-bootstrap"], check=True)
        print("UGS " + args.profile + " initialized at " + str(target))
        return 0
    except subprocess.CalledProcessError as exc: return error("command failed with status " + str(exc.returncode))

if __name__ == "__main__": sys.exit(main())
