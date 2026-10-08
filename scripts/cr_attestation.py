#!/usr/bin/env python3
"""Validate and canonicalize UGS CR reviewer/test attestations."""
import argparse
import base64
import datetime as dt
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))
import cr_model  # noqa: E402

FORMAT = "ugs-cr-attestation/v1"
NAMESPACES = {"review": "ugs-cr-review", "test": "ugs-cr-test"}
CONCLUSIONS = {"review": {"approved", "accepted", "rejected"},
               "test": {"passed", "failed"}}
OID_RE = re.compile(r"^[0-9a-f]{40}$")
PRINCIPAL_RE = re.compile(r"^[^@\s]+@[^@\s]+$")
DT_RE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?Z$")
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
BASE64_RE = re.compile(r"^[A-Za-z0-9+/]+={0,2}$")


class AttestationError(ValueError):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


def fail(code, message):
    raise AttestationError(code, message)


def reject_constant(value):
    fail("UGS-CR-ATTEST-003", "non-finite JSON number is not allowed: " + value)


def reject_duplicate(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail("UGS-CR-ATTEST-003", "duplicate JSON key: " + key)
        result[key] = value
    return result


def canonical(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True, allow_nan=False)


def load_json(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle, parse_constant=reject_constant, object_pairs_hook=reject_duplicate)
    except AttestationError:
        raise
    except (OSError, UnicodeDecodeError) as exc:
        fail("UGS-CR-ATTEST-001", "cannot read attestation: " + str(exc))
    except json.JSONDecodeError as exc:
        fail("UGS-CR-ATTEST-003", "attestation is not valid JSON: " + str(exc))


def require(condition, code, message):
    if not condition:
        fail(code, message)


def parse_time(value, field):
    require(isinstance(value, str) and DT_RE.fullmatch(value), "UGS-CR-ATTEST-004", field + " must be an RFC 3339 UTC timestamp")
    try:
        parsed = dt.datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError as exc:
        fail("UGS-CR-ATTEST-004", field + " is not a valid timestamp: " + str(exc))
    return parsed


def validate_shape(data):
    require(isinstance(data, dict), "UGS-CR-ATTEST-004", "attestation must be a JSON object")
    required = {"format", "schema_version", "type", "repository", "subject",
                "attester", "conclusion", "issued_at", "valid_from",
                "valid_until", "checks", "signature"}
    require(set(data) == required, "UGS-CR-ATTEST-004", "attestation has missing or unknown fields")
    require(data["format"] == FORMAT and data["schema_version"] == 1, "UGS-CR-ATTEST-005", "unsupported attestation format or schema version")
    kind = data["type"]
    require(kind in NAMESPACES, "UGS-CR-ATTEST-006", "attestation type must be review or test")
    require(isinstance(data["repository"], str) and bool(data["repository"]), "UGS-CR-ATTEST-007", "repository is required")

    subject = data["subject"]
    require(isinstance(subject, dict), "UGS-CR-ATTEST-008", "subject must be an object")
    subject_required = {"cr_id", "revision", "binding_sha256", "target_ref", "strategy", "result_oid", "scope"}
    require(set(subject) == subject_required, "UGS-CR-ATTEST-008", "subject has missing or unknown fields")
    require(isinstance(subject["cr_id"], str) and re.fullmatch(r"CR-[0-9]{4}", subject["cr_id"]), "UGS-CR-ATTEST-009", "subject CR id is invalid")
    require(isinstance(subject["revision"], int) and not isinstance(subject["revision"], bool) and subject["revision"] > 0, "UGS-CR-ATTEST-009", "subject revision is invalid")
    require(isinstance(subject["binding_sha256"], str) and SHA256_RE.fullmatch(subject["binding_sha256"]), "UGS-CR-ATTEST-009", "subject binding is invalid")
    require(isinstance(subject["target_ref"], str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]*", subject["target_ref"]), "UGS-CR-ATTEST-009", "subject target ref is invalid")
    require(subject["strategy"] in {"rebase-ff", "merge", "squash"}, "UGS-CR-ATTEST-009", "subject integration strategy is invalid")
    require(subject["result_oid"] is None or (isinstance(subject["result_oid"], str) and OID_RE.fullmatch(subject["result_oid"])), "UGS-CR-ATTEST-009", "subject result OID is invalid")
    require(subject["scope"] in {"source", "integrated-result"}, "UGS-CR-ATTEST-009", "subject scope is invalid")

    attester = data["attester"]
    require(isinstance(attester, dict) and set(attester) == {"principal", "role"}, "UGS-CR-ATTEST-010", "attester has missing or unknown fields")
    require(isinstance(attester["principal"], str) and PRINCIPAL_RE.fullmatch(attester["principal"]), "UGS-CR-ATTEST-010", "attester principal is invalid")
    require(attester["role"] in {"reviewer", "tester"}, "UGS-CR-ATTEST-010", "attester role is invalid")
    require(data["conclusion"] in CONCLUSIONS[kind], "UGS-CR-ATTEST-011", "conclusion is invalid for attestation type")
    issued = parse_time(data["issued_at"], "issued_at")
    valid_from = parse_time(data["valid_from"], "valid_from")
    valid_until = None if data["valid_until"] is None else parse_time(data["valid_until"], "valid_until")
    require(valid_from <= issued and (valid_until is None or issued <= valid_until), "UGS-CR-ATTEST-012", "issued_at is outside the validity window")
    require(kind == "review" and attester["role"] == "reviewer" or kind == "test" and attester["role"] == "tester", "UGS-CR-ATTEST-013", "attester role does not match attestation type")

    checks = data["checks"]
    require(isinstance(checks, list) and len(checks) > 0, "UGS-CR-ATTEST-014", "checks must be a non-empty array")
    for check in checks:
        if isinstance(check, str):
            require(bool(check), "UGS-CR-ATTEST-014", "check names must not be empty")
        else:
            require(isinstance(check, dict) and set(check).issubset({"name", "status", "evidence"}) and set(check) >= {"name", "status"}, "UGS-CR-ATTEST-014", "check object is invalid")
            require(isinstance(check["name"], str) and bool(check["name"]) and check["status"] in {"passed", "failed", "skipped"}, "UGS-CR-ATTEST-014", "check name or status is invalid")
            if "evidence" in check:
                require(isinstance(check["evidence"], list) and all(isinstance(item, str) and item for item in check["evidence"]), "UGS-CR-ATTEST-014", "check evidence is invalid")

    signature = data["signature"]
    require(isinstance(signature, dict) and set(signature) == {"format", "namespace", "principal", "value"}, "UGS-CR-ATTEST-015", "signature has missing or unknown fields")
    require(signature["format"] == "ssh" and signature["namespace"] in set(NAMESPACES.values()), "UGS-CR-ATTEST-015", "signature metadata is invalid")
    require(signature["namespace"] == NAMESPACES[kind], "UGS-CR-ATTEST-016", "signature namespace does not match attestation type")
    require(isinstance(signature["principal"], str) and PRINCIPAL_RE.fullmatch(signature["principal"]), "UGS-CR-ATTEST-015", "signature principal is invalid")
    require(signature["principal"] == attester["principal"], "UGS-CR-ATTEST-016", "attester principal must equal signature principal")
    require(isinstance(signature["value"], str) and BASE64_RE.fullmatch(signature["value"]), "UGS-CR-ATTEST-015", "signature value is not base64")
    try:
        base64.b64decode(signature["value"], validate=True)
    except Exception:
        fail("UGS-CR-ATTEST-015", "signature value is not base64")
    return issued


def repository_root():
    try:
        return Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True, stderr=subprocess.DEVNULL).strip())
    except Exception:
        return Path.cwd()


def validate_cr(data, cr_path):
    try:
        model = cr_model.parse_path(cr_path)
    except cr_model.CRModelError as exc:
        fail("UGS-CR-ATTEST-020", "referenced CR is invalid: " + exc.message)
    require(model["format"] == "ugs-cr/v1" and model["schema_version"] == 1,
            "UGS-CR-ATTEST-020",
            "CR attestations require a canonical ugs-cr/v1 record")
    subject = data["subject"]
    integration = model["integration"]
    require(subject["cr_id"] == model["id"], "UGS-CR-ATTEST-021", "subject CR id does not match CR")
    require(subject["revision"] == model["revision"], "UGS-CR-ATTEST-021", "subject revision does not match CR")
    require(subject["binding_sha256"] == model["binding"]["sha256"], "UGS-CR-ATTEST-021", "subject binding does not match CR")
    require(subject["target_ref"] == integration["target_ref"] and subject["strategy"] == integration["strategy"], "UGS-CR-ATTEST-022", "subject integration does not match CR")
    result_oid = integration["result_oid"]
    if subject["scope"] == "source":
        require(subject["result_oid"] is None, "UGS-CR-ATTEST-023", "source attestation must not claim an integrated result")
    else:
        require(result_oid is not None and subject["result_oid"] == result_oid, "UGS-CR-ATTEST-023", "integrated-result attestation does not match CR result")
    if model["status"] == "integrated":
        require(result_oid is not None, "UGS-CR-ATTEST-023", "integrated CR has no result")
    else:
        require(subject["result_oid"] is None, "UGS-CR-ATTEST-023",
                "non-integrated CR cannot have an integrated result")


def signer_lines(principal, namespace, allowed_file):
    if not allowed_file.is_file():
        fail("UGS-CR-ATTEST-030", "allowed signers file does not exist")
    candidates = []
    try:
        lines = allowed_file.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeDecodeError) as exc:
        fail("UGS-CR-ATTEST-030", "cannot read allowed signers file: " + str(exc))
    for raw in lines:
        line = raw.strip()
        if not line or line.startswith("#") or not line.startswith(principal + " "):
            continue
        match = re.search(r'(?:(?:^| )namespaces="([^"]+)")', line)
        if not match or namespace not in match.group(1).split(","):
            continue
        parts = line.split()
        key_index = next((index for index, token in enumerate(parts)
                          if token.startswith(("ssh-", "ecdsa-", "sk-"))), None)
        if key_index is not None and key_index + 1 < len(parts):
            # Keep the complete public-key pair for fingerprint and KRL checks.
            public_key = " ".join(parts[key_index:key_index + 2])
            candidates.append((line, public_key))
    require(candidates, "UGS-CR-ATTEST-031",
            "principal is not authorized for attestation namespace")
    return candidates


def validate_role(principal, kind, issued, roles_path, fingerprint):
    roles = load_json(roles_path)
    require(isinstance(roles, dict) and isinstance(roles.get("signers"), list), "UGS-CR-ATTEST-032", "signer roles file is invalid")
    principal_entries = [item for item in roles["signers"]
                         if isinstance(item, dict) and item.get("principal") == principal]
    require(principal_entries, "UGS-CR-ATTEST-033", "principal is not registered in signer roles")
    candidates = []
    for item in principal_entries:
        if item.get("key_fingerprint") != fingerprint:
            continue
        start = item.get("effective_from")
        end = item.get("effective_until")
        try:
            start_date = dt.date.fromisoformat(start)
            end_date = None if end is None else dt.date.fromisoformat(end)
        except (TypeError, ValueError):
            fail("UGS-CR-ATTEST-032", "signer role validity dates are invalid")
        if end_date is not None and start_date >= end_date:
            fail("UGS-CR-ATTEST-032", "signer role validity dates are inverted")
        if item.get("status") == "revoked" and end_date is None:
            fail("UGS-CR-ATTEST-032", "revoked signer role requires effective_until")
        candidates.append((item, start_date, end_date))
    require(candidates, "UGS-CR-ATTEST-036", "signer key does not match a registered role")
    issued_date = issued.date()
    accepted = {"maintainer", "reviewer"} if kind == "review" else {"maintainer", "tester"}
    active = []
    revoked_after_window = False
    for item, start, end in candidates:
        require(item.get("status") in {"active", "revoked"},
                "UGS-CR-ATTEST-032", "signer status is invalid")
        if item.get("role") not in accepted:
            continue
        if start <= issued_date and (end is None or issued_date < end):
            active.append(item)
        elif item.get("status") == "revoked" and end is not None and issued_date >= end:
            revoked_after_window = True
    if not active:
        if not any(item.get("role") in accepted for item, _start, _end in candidates):
            fail("UGS-CR-ATTEST-033", "signer role is not authorized for attestation type")
        if revoked_after_window:
            fail("UGS-CR-ATTEST-035", "signer role is revoked for this attestation date")
        fail("UGS-CR-ATTEST-034", "attestation is outside signer role validity window")
    require(len(active) == 1, "UGS-CR-ATTEST-032",
            "signer role validity windows overlap")
    entry = active[0]
    return entry


def key_fingerprint(public_key, path):
    path.write_text(public_key + "\n", encoding="utf-8")
    try:
        return subprocess.check_output(
            ["ssh-keygen", "-lf", str(path), "-E", "sha256"],
            text=True, stderr=subprocess.DEVNULL).split()[1]
    except (OSError, subprocess.CalledProcessError, IndexError):
        fail("UGS-CR-ATTEST-036", "cannot determine allowed signer fingerprint")


def key_is_revoked(revoked, public_key, fingerprint, tmp_path, key_path):
    if not revoked.is_file():
        fail("UGS-CR-ATTEST-038", "revocation file does not exist")
    try:
        raw = revoked.read_bytes()
    except OSError as exc:
        fail("UGS-CR-ATTEST-038", "cannot read revocation file: " + str(exc))
    if not raw.strip():
        return False
    # Git/OpenSSH deployments use either a text list of public keys or a
    # binary KRL. Check text keys directly, then fall back to ssh-keygen -Q.
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        text = None
    if text is not None:
        parsed_key = False
        saw_content = False
        for line_number, line in enumerate(text.splitlines()):
            parts = line.strip().split()
            if not parts or parts[0].startswith("#"):
                continue
            saw_content = True
            index = next((i for i, token in enumerate(parts)
                          if token.startswith(("ssh-", "ecdsa-", "sk-"))), None)
            if index is None or index + 1 >= len(parts):
                continue
            parsed_key = True
            candidate = " ".join(parts[index:index + 2])
            candidate_path = tmp_path / ("revoked-key-%d.pub" % line_number)
            try:
                candidate_fp = key_fingerprint(candidate, candidate_path)
            except AttestationError:
                continue
            if candidate_fp == fingerprint:
                return True
        if parsed_key:
            return False
        if not saw_content:
            return False
    try:
        result = subprocess.run(
            ["ssh-keygen", "-Q", "-f", str(revoked), str(key_path)],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
    except OSError:
        fail("UGS-CR-ATTEST-038", "ssh-keygen is required for revocation checks")
    if result.returncode == 0:
        return False
    if result.returncode == 1:
        return True
    fail("UGS-CR-ATTEST-038", "revocation file cannot be verified")


def revocation_uses_krl(revoked):
    try:
        raw = revoked.read_bytes()
    except OSError as exc:
        fail("UGS-CR-ATTEST-038", "cannot read revocation file: " + str(exc))
    if not raw.strip():
        return False
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        return True
    saw_content = False
    for line in text.splitlines():
        parts = line.strip().split()
        if not parts or parts[0].startswith("#"):
            continue
        saw_content = True
        if any(token.startswith(("ssh-", "ecdsa-", "sk-"))
               for token in parts):
            return False
    # A comments-only text registry is an empty raw-key revocation list, not
    # a malformed binary KRL.
    return saw_content


def verify_signature(data, root, issued):
    signature = data["signature"]
    allowed = Path(os.environ.get("UGS_ALLOWED_SIGNERS_FILE", str(root / "keys/allowed_signers")))
    roles = Path(os.environ.get("UGS_SIGNER_ROLES_FILE", str(root / "keys/signer_roles.json")))
    candidates = signer_lines(signature["principal"], signature["namespace"], allowed)
    with tempfile.TemporaryDirectory(prefix="ugs-cr-attestation-") as tmp:
        tmp_path = Path(tmp)
        signature_path = tmp_path / "signature"
        payload_path = tmp_path / "payload"
        try:
            decoded = base64.b64decode(signature["value"], validate=True)
        except Exception:
            fail("UGS-CR-ATTEST-015", "signature value is not base64")
        signature_path.write_bytes(decoded)
        payload_path.write_bytes(canonical({key: value for key, value in data.items() if key != "signature"}).encode("utf-8"))
        revoked = Path(os.environ.get("UGS_REVOKED_SIGNERS_FILE", str(root / "keys/revoked_signers")))
        use_krl = revocation_uses_krl(revoked)
        role_errors = []
        signature_failed = False
        for index, (line, public_key) in enumerate(candidates):
            key_path = tmp_path / ("key-%d.pub" % index)
            fingerprint = key_fingerprint(public_key, key_path)
            try:
                entry = validate_role(signature["principal"], data["type"], issued, roles, fingerprint)
            except AttestationError as exc:
                role_errors.append(exc)
                continue
            historical = False
            if entry.get("status") == "revoked" and entry.get("effective_until"):
                historical = issued.date() < dt.date.fromisoformat(entry["effective_until"])
            if not historical and key_is_revoked(revoked, public_key, fingerprint, tmp_path, key_path):
                role_errors.append(AttestationError("UGS-CR-ATTEST-035", "signer key is revoked"))
                continue
            candidate_allowed = tmp_path / ("allowed-%d" % index)
            candidate_allowed.write_text(line + "\n", encoding="utf-8")
            try:
                verify_args = ["ssh-keygen", "-Y", "verify", "-f", str(candidate_allowed),
                               "-I", signature["principal"], "-n", signature["namespace"],
                               "-s", str(signature_path)]
                if use_krl and not historical:
                    verify_args.extend(["-r", str(revoked)])
                subprocess.run(verify_args,
                    stdin=payload_path.open("rb"), stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL, check=True)
                return
            except (OSError, subprocess.CalledProcessError):
                signature_failed = True
                continue
        if signature_failed:
            fail("UGS-CR-ATTEST-037", "SSH attestation signature verification failed")
        if role_errors:
            raise role_errors[-1]
        fail("UGS-CR-ATTEST-037", "SSH attestation signature verification failed")


def validate(path, cr_path, expected_repository=None, verify=True):
    data = load_json(path)
    issued = validate_shape(data)
    if expected_repository is not None:
        require(data["repository"] == expected_repository, "UGS-CR-ATTEST-018", "attestation repository does not match expected repository")
    validate_cr(data, cr_path)
    if verify:
        verify_signature(data, repository_root(), issued)
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("attestation")
    parser.add_argument("--cr-file", "--cr", dest="cr_path", required=True, help="referenced CR Markdown record")
    parser.add_argument("--repository")
    parser.add_argument("--no-signature", action="store_true", help="validate shape and CR binding without SSH verification")
    parser.add_argument("--payload", action="store_true", help="write canonical signed payload after validation")
    args = parser.parse_args()
    try:
        data = validate(args.attestation, args.cr_path, args.repository, not args.no_signature)
        if args.payload:
            sys.stdout.write(canonical({key: value for key, value in data.items() if key != "signature"}))
        else:
            print("CR attestation validation passed")
        return 0
    except AttestationError as exc:
        if os.environ.get("UGS_ERROR_FORMAT") == "json":
            print(json.dumps({"format": "ugs-error/v1", "code": exc.code, "message": exc.message}, ensure_ascii=False, sort_keys=True), file=sys.stderr)
        else:
            print(exc.code + ": " + exc.message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
