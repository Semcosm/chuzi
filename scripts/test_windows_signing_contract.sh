#!/usr/bin/env bash
set -euo pipefail
export PYTHONDONTWRITEBYTECODE=1

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

python3 - "$repo_root" "$test_root" <<'PY'
import json
import hashlib
import importlib.util
import subprocess
import sys
import zipfile
from pathlib import Path

repo, root = map(Path, sys.argv[1:])
policy_path = repo / "packaging/windows/authenticode-policy.json"
policy = json.loads(policy_path.read_text(encoding="utf-8"))
assert policy["required_timestamp"] is True
assert policy["artifact_kind"] == "payload"
assert "chuzi-session-broker.exe" in policy["production_required_files"]
assert policy["allowed_thumbprints"] == []

stage = root / "stage"
(stage / "browser-worker").mkdir(parents=True)
for name in set(policy["production_required_files"] + ["PresentMon.exe", "PresentMon-LICENSE.txt"]):
    (stage / name).write_bytes(name.encode())
(stage / "browser-worker" / "worker-manifest.json").write_text(
    json.dumps({"protocol": "v1", "adapters": []}), encoding="utf-8"
)
evidence = stage / "authenticode-signing.json"
subprocess.run([
    sys.executable, str(repo / "scripts/prepare_windows_signing_metadata.py"),
    "--stage", str(stage), "--policy", str(policy_path), "--output", str(evidence),
], check=True)
resolved = json.loads((stage / "authenticode-policy.json").read_text(encoding="utf-8"))
assert resolved["status"] == "unsigned-development"
assert resolved["production_release_allowed"] is False

result = subprocess.run([
    sys.executable, str(repo / "scripts/generate_release_manifest.py"),
    "--stage", str(stage), "--target", "windows-amd64",
    "--version", "v1.2.3", "--commit", "a" * 40, "--channel", "stable",
], text=True, capture_output=True)
assert result.returncode != 0, result.stdout
assert "production Authenticode signer is not configured" in result.stderr, result.stderr

resolved["allowed_thumbprints"] = ["A" * 40]
resolved["artifact_kind"] = "payload"
resolved["status"] = "production-signed"
resolved["production_release_allowed"] = True
resolved["signing_order"] = ["authenticode", "release-manifest", "archive", "release-index", "attestation"]
(stage / "authenticode-policy.json").write_text(json.dumps(resolved), encoding="utf-8")
evidence_data = {
    "format": "chuzi-authenticode-evidence/v1",
    "status": "production-signed",
    "production_release_allowed": True,
    "artifact_kind": "payload",
    "publisher_subject_prefix": policy["publisher_subject_prefix"],
    "allowed_thumbprints": ["A" * 40],
    "files": [{"path": name, "thumbprint": "A" * 40, "timestamp_present": True, "sha256": "0" * 64} for name in policy["production_required_files"]],
    "signing_order": resolved["signing_order"],
    "hashes_computed_after_signing": False,
    "timestamp": {"required": True, "present": False},
}
evidence.write_text(json.dumps(evidence_data), encoding="utf-8")
result = subprocess.run([
    sys.executable, str(repo / "scripts/generate_release_manifest.py"),
    "--stage", str(stage), "--target", "windows-amd64",
    "--version", "v1.2.3", "--commit", "a" * 40, "--channel", "stable",
], text=True, capture_output=True)
assert result.returncode != 0
assert "production Authenticode evidence is invalid" in result.stderr

evidence_data["hashes_computed_after_signing"] = True
evidence_data["timestamp"] = {"required": True, "present": True}
for record in evidence_data["files"]:
    record["sha256"] = hashlib.sha256((stage / record["path"]).read_bytes()).hexdigest()
evidence.write_text(json.dumps(evidence_data), encoding="utf-8")
result = subprocess.run([
    sys.executable, str(repo / "scripts/generate_release_manifest.py"),
    "--stage", str(stage), "--target", "windows-amd64",
    "--version", "v1.2.3", "--commit", "a" * 40, "--channel", "stable",
], text=True, capture_output=True)
assert result.returncode == 0, result.stderr

(stage / "chuzi-session-broker.exe").unlink()
result = subprocess.run([
    sys.executable, str(repo / "scripts/generate_release_manifest.py"),
    "--stage", str(stage), "--target", "windows-amd64",
    "--version", "v1.2.3", "--commit", "a" * 40, "--channel", "stable",
], text=True, capture_output=True)
assert result.returncode != 0
assert "production Windows helper files are missing" in result.stderr

validator_spec = importlib.util.spec_from_file_location(
    "validate_nightly_artifact", repo / "scripts/validate_nightly_artifact.py"
)
validator = importlib.util.module_from_spec(validator_spec)
validator_spec.loader.exec_module(validator)
bundle = root / "signed-bundle.zip"
production_files = list(policy["production_required_files"])
bundle_policy = dict(policy)
bundle_policy.update({
    "allowed_thumbprints": ["A" * 40],
    "artifact_kind": "payload",
    "status": "production-signed",
    "production_release_allowed": True,
    "signing_order": ["authenticode", "release-manifest", "archive", "release-index", "attestation"],
})
bundle_members = {name: name.encode() for name in production_files}
bundle_members["authenticode-policy.json"] = json.dumps(bundle_policy).encode()
bundle_evidence = {
    "format": "chuzi-authenticode-evidence/v1",
    "status": "production-signed",
    "production_release_allowed": True,
    "artifact_kind": "payload",
    "signing_order": ["authenticode", "release-manifest", "archive", "release-index", "attestation"],
    "publisher_subject_prefix": policy["publisher_subject_prefix"],
    "allowed_thumbprints": ["A" * 40],
    "files": [{
        "path": name, "thumbprint": "A" * 40, "timestamp_present": True,
        "sha256": hashlib.sha256(bundle_members[name]).hexdigest(),
    } for name in production_files],
    "signing_order": ["authenticode", "release-manifest", "archive", "release-index", "attestation"],
    "hashes_computed_after_signing": True,
    "timestamp": {"required": True, "present": True},
}
bundle_members["authenticode-signing.json"] = json.dumps(bundle_evidence).encode()
def write_bundle(destination, members):
    with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name, data in members.items():
            archive.writestr(name, data)
write_bundle(bundle, bundle_members)
validator.validate_windows_signing(bundle, "zip", {}, "stable")
tampered_evidence = dict(bundle_evidence)
tampered_evidence["files"] = [dict(item) for item in bundle_evidence["files"]]
tampered_evidence["files"][0]["sha256"] = "0" * 64
tampered_members = dict(bundle_members)
tampered_members["authenticode-signing.json"] = json.dumps(tampered_evidence).encode()
write_bundle(root / "tampered-evidence.zip", tampered_members)
try:
    validator.validate_windows_signing(root / "tampered-evidence.zip", "zip", {}, "stable")
except ValueError:
    pass
else:
    raise AssertionError("validator accepted tampered Authenticode evidence")
wrong_order = dict(bundle_evidence)
wrong_order["signing_order"] = list(reversed(bundle_evidence["signing_order"]))
wrong_order_members = dict(bundle_members)
wrong_order_members["authenticode-signing.json"] = json.dumps(wrong_order).encode()
write_bundle(root / "wrong-order.zip", wrong_order_members)
try:
    validator.validate_windows_signing(root / "wrong-order.zip", "zip", {}, "stable")
except ValueError:
    pass
else:
    raise AssertionError("validator accepted an invalid signing order")
wrong_signer = dict(bundle_evidence)
wrong_signer["files"] = [dict(item) for item in bundle_evidence["files"]]
wrong_signer["files"][0]["thumbprint"] = "B" * 40
wrong_signer_members = dict(bundle_members)
wrong_signer_members["authenticode-signing.json"] = json.dumps(wrong_signer).encode()
write_bundle(root / "wrong-signer.zip", wrong_signer_members)
try:
    validator.validate_windows_signing(root / "wrong-signer.zip", "zip", {}, "stable")
except ValueError:
    pass
else:
    raise AssertionError("validator accepted a signer outside the allowlist")
tampered_members = dict(bundle_members)
tampered_members[production_files[0]] = b"tampered payload"
write_bundle(root / "tampered-payload.zip", tampered_members)
try:
    validator.validate_windows_signing(root / "tampered-payload.zip", "zip", {}, "stable")
except ValueError:
    pass
else:
    raise AssertionError("validator accepted a payload/evidence hash mismatch")
PY

echo "Windows signing contract passed"
