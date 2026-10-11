#!/usr/bin/env python3
"""Create explicit development Authenticode metadata for non-Windows builds.

Windows CI replaces the empty signer list and unsigned status after signing.
Keeping this metadata in every Windows stage prevents an unsigned development
archive from being mistaken for a production package.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", required=True, type=Path)
    parser.add_argument("--policy", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    policy = json.loads(args.policy.read_text(encoding="utf-8"))
    if policy.get("format") != "chuzi-authenticode-policy/v1":
        raise SystemExit("invalid Authenticode policy format")
    required = policy.get("required_files")
    if not isinstance(required, list) or not required:
        raise SystemExit("Authenticode policy required_files is empty")
    args.stage.mkdir(parents=True, exist_ok=True)
    resolved = dict(policy)
    resolved["allowed_thumbprints"] = []
    resolved["artifact_kind"] = "payload"
    resolved["status"] = "unsigned-development"
    resolved["signing_order"] = [
        "authenticode",
        "release-manifest",
        "archive",
        "release-index",
        "attestation",
    ]
    resolved["production_release_allowed"] = False
    (args.stage / "authenticode-policy.json").write_text(
        json.dumps(resolved, indent=2) + "\n", encoding="utf-8"
    )
    evidence = {
        "format": "chuzi-authenticode-evidence/v1",
        "status": "unsigned-development",
        "production_release_allowed": False,
        "artifact_kind": "payload",
        "publisher_subject_prefix": policy["publisher_subject_prefix"],
        "allowed_thumbprints": [],
        "files": [],
        "signing_order": resolved["signing_order"],
        "hashes_computed_after_signing": False,
        "timestamp": {"required": True, "present": False},
    }
    args.output.write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
