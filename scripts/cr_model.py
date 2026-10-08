#!/usr/bin/env python3
"""Parse, render, and project UGS change-request Markdown records."""
import argparse
import hashlib
import json
import os
import re
import sys
from pathlib import Path


SCHEMA = "schema/cr.schema.json"
V1_FORMAT = "ugs-cr/v1"
LEGACY_FORMAT = "ugs-cr/legacy-v0"
SECTIONS = (
    "Summary",
    "Motivation",
    "Test Evidence",
    "Risk",
    "Rollback",
    "Breaking Change",
    "Backport Target",
)
V1_METADATA = (
    "Format",
    "Schema Version",
    "Base",
    "Head or Range",
    "Integration Target",
    "Integration Strategy",
    "Review Evidence",
    "Title",
    "Revision",
    "Status",
    "Decision",
    "Policy Version",
    "Base OID",
    "Head OID",
    "Integrated Result",
    "Coverage OIDs",
    "Extensions",
)
LEGACY_REQUIRED = (
    "Base",
    "Head or Range",
    "Title",
    "Revision",
    "Status",
    "Decision",
    "Policy Version",
    "Base OID",
    "Head OID",
    "Integrated Result",
)
KNOWN_METADATA = set(V1_METADATA) | {"Coverage OIDs"}
OID = re.compile(r"^[0-9a-f]{40}$")
CR_ID = re.compile(r"^# (CR-[0-9]{4}): (.+)$")
POLICY_VERSION = re.compile(r"^v(0\.2|0\.3(-[0-9]+|-(draft-[0-9]+|rc-[0-9]+))?)$")
TARGET_REF = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]*$")


class CRModelError(ValueError):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


def fail(code, message):
    raise CRModelError(code, message)


def canonical_json(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def reject_nonstandard_json(value):
    raise ValueError("non-standard JSON constant: " + value)


def non_placeholder(value, name):
    if not isinstance(value, str) or not value:
        fail("UGS-CR-002", "missing " + name + ": line")
    if re.fullmatch(r"<[^>]+>", value):
        fail("UGS-CR-003", name + ": must not use an unfilled template placeholder")


def parse_metadata_block(block, strict):
    metadata = {}
    for line in block.splitlines():
        if not line:
            if strict:
                fail("UGS-CR-021", "v1 metadata must not contain blank lines")
            continue
        if ": " not in line:
            if strict:
                fail("UGS-CR-018", "unknown v1 metadata line: " + line)
            continue
        key, value = line.split(": ", 1)
        if key not in KNOWN_METADATA:
            if strict:
                fail("UGS-CR-018", "unknown v1 metadata field: " + key)
            continue
        if key in metadata:
            fail("UGS-CR-016", "duplicate metadata field: " + key)
        metadata[key] = value
    return metadata


def section_matches(text):
    alternatives = "|".join(re.escape(section) for section in SECTIONS)
    return list(re.finditer(r"^## (" + alternatives + r")\n", text, re.MULTILINE))


def parse_sections(text, start, strict):
    suffix = text[start:]
    matches = section_matches(suffix)
    if not matches:
        fail("UGS-CR-005", "missing section: ## Summary")
    if strict:
        all_h2 = re.findall(r"^## (.+)$", suffix, re.MULTILINE)
        for heading in all_h2:
            if heading not in SECTIONS:
                fail("UGS-CR-020", "unknown v1 section: ## " + heading)
    counts = {section: 0 for section in SECTIONS}
    for match in matches:
        counts[match.group(1)] += 1
    for section in SECTIONS:
        if not counts[section]:
            fail("UGS-CR-005", "missing section: ## " + section)
        if counts[section] > 1:
            fail("UGS-CR-020", "duplicate section: ## " + section)
    headings = [match.group(1) for match in matches]
    if strict and tuple(headings) != SECTIONS:
        fail("UGS-CR-020", "v1 sections must use the canonical order")
    values = {}
    for index, match in enumerate(matches):
        section = match.group(1)
        body_start = match.end()
        if body_start >= len(suffix) or suffix[body_start] != "\n":
            fail("UGS-CR-021", "section must have one blank line after heading: ## " + section)
        body_start += 1
        if index + 1 < len(matches):
            body_end = matches[index + 1].start()
            block = suffix[body_start:body_end]
            if not block.endswith("\n\n"):
                fail("UGS-CR-021", "sections must be separated by one blank line")
            body = block[:-2]
        else:
            block = suffix[body_start:]
            if not block.endswith("\n"):
                fail("UGS-CR-021", "record must end with LF")
            body = block[:-1]
        if not any(line.strip() and not re.fullmatch(r"<[^>]+>", line.strip()) for line in body.splitlines()):
            fail("UGS-CR-006", "section must include non-placeholder content: ## " + section)
        values[section] = body
    return values


def validate_common(metadata, strict):
    for field in LEGACY_REQUIRED:
        non_placeholder(metadata.get(field), field)
    revision = metadata["Revision"]
    if not re.fullmatch(r"[1-9][0-9]*", revision):
        fail("UGS-CR-017", "Revision must be a positive integer")
    status = metadata["Status"]
    if status not in {"pending", "accepted", "integrated", "rejected", "superseded"}:
        fail("UGS-CR-017", "Status is invalid")
    decision = metadata["Decision"]
    if decision not in {"accepted", "rejected", "superseded", "pending"}:
        fail("UGS-CR-017", "Decision is invalid")
    if not POLICY_VERSION.fullmatch(metadata["Policy Version"]):
        fail("UGS-CR-017", "Policy Version is invalid")
    for field in ("Base OID", "Head OID"):
        if not OID.fullmatch(metadata[field]):
            fail("UGS-CR-007", "CR object IDs must be full lowercase SHA-1 values")
    # Legacy records predate Integration Target and retain their historical
    # main-only provenance rule.  v1 validates the result against its declared
    # target in result_parts(), below.
    if not strict:
        result = metadata["Integrated Result"]
        if result != "pending" and not re.fullmatch(r"main@[0-9a-f]{40}", result):
            fail("UGS-CR-010", "Integrated Result must match main@<full commit OID>")
        if status == "integrated" and result == "pending":
            fail("UGS-CR-009", "integrated CRs must have a main@<full commit OID> result")


def parse_coverage(value, strict):
    if value is None:
        return []
    if strict and value == "none":
        return []
    if not value:
        fail("UGS-CR-017", "Coverage OIDs is invalid")
    values = value.split()
    if any(not OID.fullmatch(item) for item in values):
        fail("UGS-CR-017", "Coverage OIDs must contain full lowercase SHA-1 values")
    if len(set(values)) != len(values):
        fail("UGS-CR-017", "Coverage OIDs must not contain duplicates")
    if strict and values != sorted(values):
        fail("UGS-CR-021", "Coverage OIDs must use lexical order")
    return values


def result_parts(value, target_ref, strict):
    if value == "pending":
        return None, None
    if strict:
        match = re.fullmatch(r"([A-Za-z0-9][A-Za-z0-9._/-]*)@([0-9a-f]{40})", value)
        if not match or match.group(1) != target_ref:
            fail("UGS-CR-017", "Integrated Result must match Integration Target and a full lowercase SHA-1")
        return match.group(1), match.group(2)
    return "main", value.removeprefix("main@")


def binding(model):
    payload = {key: value for key, value in model.items() if key != "binding"}
    digest = hashlib.sha256(canonical_json(payload).encode("utf-8")).hexdigest()
    return {"format": "ugs-cr-binding/v1", "sha256": "sha256:" + digest}


def build_model(identifier, metadata, sections, format_name, schema_version, strict):
    validate_common(metadata, strict)
    heading_title = identifier[1]
    cr_id = identifier[0]
    if strict:
        expected = set(V1_METADATA)
        missing = [field for field in V1_METADATA if field not in metadata]
        if missing:
            fail("UGS-CR-002", "missing " + missing[0] + ": line")
        if metadata["Format"] != V1_FORMAT:
            fail("UGS-CR-014", "unsupported CR format")
        if metadata["Schema Version"] != "1":
            fail("UGS-CR-015", "unsupported CR schema version")
        if not TARGET_REF.fullmatch(metadata["Integration Target"]):
            fail("UGS-CR-017", "Integration Target is invalid")
        if metadata["Integration Strategy"] not in {"rebase-ff", "merge", "squash"}:
            fail("UGS-CR-017", "Integration Strategy is invalid")
        if metadata["Review Evidence"] not in {"none", "trailers"}:
            fail("UGS-CR-017", "Review Evidence is invalid")
        if heading_title != metadata["Title"]:
            fail("UGS-CR-004", "title must match # CR-XXXX: <title>")
        try:
            extensions = json.loads(metadata["Extensions"], parse_constant=reject_nonstandard_json)
        except (json.JSONDecodeError, ValueError) as exc:
            fail("UGS-CR-019", "Extensions must be a JSON object: " + str(exc))
        if not isinstance(extensions, dict) or any(not isinstance(key, str) or not key.startswith("x-") for key in extensions):
            fail("UGS-CR-019", "Extensions keys must begin with x-")
        target_ref = metadata["Integration Target"]
        strategy = metadata["Integration Strategy"]
        review_evidence = metadata["Review Evidence"]
    else:
        extensions = {}
        target_ref = "main"
        strategy = metadata.get("Integration Strategy")
        if strategy is not None and strategy not in {"rebase-ff", "merge", "squash"}:
            fail("UGS-CR-017", "Integration Strategy is invalid")
        review_evidence = metadata.get("Review Evidence")
        if review_evidence is not None and review_evidence != "trailers":
            fail("UGS-CR-017", "Review Evidence is invalid")
    coverage = parse_coverage(metadata.get("Coverage OIDs"), strict)
    result_ref, result_oid = result_parts(metadata["Integrated Result"], target_ref, strict)
    if strict:
        lifecycle = {
            "pending": ("pending", True),
            "accepted": ("accepted", True),
            "integrated": ("accepted", False),
            "rejected": ("rejected", True),
            "superseded": ("superseded", True),
        }
        decision, result_pending = lifecycle[metadata["Status"]]
        if metadata["Decision"] != decision or (result_oid is None) != result_pending:
            fail("UGS-CR-022", "Status, Decision, and Integrated Result do not form a valid lifecycle state")
    model = {
        "$schema": SCHEMA,
        "format": format_name,
        "schema_version": schema_version,
        "id": cr_id,
        "title": metadata["Title"],
        "revision": int(metadata["Revision"]),
        "status": metadata["Status"],
        "decision": metadata["Decision"],
        "policy_version": metadata["Policy Version"],
        "source": {
            "base_ref": metadata["Base"],
            "head_or_range": metadata["Head or Range"],
            "base_oid": metadata["Base OID"],
            "head_oid": metadata["Head OID"],
        },
        "integration": {
            "strategy": strategy,
            "target_ref": target_ref,
            "review_evidence": review_evidence,
            "result_ref": result_ref,
            "result_oid": result_oid,
        },
        "coverage_oids": coverage,
        "sections": sections,
        "extensions": extensions,
    }
    model["binding"] = binding(model)
    return model


def render(model):
    if model.get("format") != V1_FORMAT or model.get("schema_version") != 1:
        fail("UGS-CR-014", "only ugs-cr/v1 records can be rendered")
    integration = model["integration"]
    result = "pending" if integration["result_oid"] is None else integration["result_ref"] + "@" + integration["result_oid"]
    coverage = "none" if not model["coverage_oids"] else " ".join(model["coverage_oids"])
    values = {
        "Format": V1_FORMAT,
        "Schema Version": "1",
        "Base": model["source"]["base_ref"],
        "Head or Range": model["source"]["head_or_range"],
        "Integration Target": integration["target_ref"],
        "Integration Strategy": integration["strategy"],
        "Review Evidence": integration["review_evidence"],
        "Title": model["title"],
        "Revision": str(model["revision"]),
        "Status": model["status"],
        "Decision": model["decision"],
        "Policy Version": model["policy_version"],
        "Base OID": model["source"]["base_oid"],
        "Head OID": model["source"]["head_oid"],
        "Integrated Result": result,
        "Coverage OIDs": coverage,
        "Extensions": canonical_json(model["extensions"]),
    }
    output = "# " + model["id"] + ": " + model["title"] + "\n\n"
    output += "\n".join(key + ": " + values[key] for key in V1_METADATA)
    output += "\n\n"
    for index, section in enumerate(SECTIONS):
        output += "## " + section + "\n\n" + model["sections"][section]
        output += "\n" if index == len(SECTIONS) - 1 else "\n\n"
    return output


def parse_text(text):
    if not text:
        fail("UGS-CR-001", "record file does not exist or is empty")
    lines = text.splitlines()
    if not lines:
        fail("UGS-CR-004", "title must match # CR-XXXX: <title>")
    header = CR_ID.fullmatch(lines[0])
    if not header:
        fail("UGS-CR-004", "title must match # CR-XXXX: <title>")
    identifier = (header.group(1), header.group(2))
    matches = section_matches(text)
    if not matches:
        fail("UGS-CR-005", "missing section: ## Summary")
    preamble_start = len(lines[0]) + 1
    preamble = text[preamble_start:matches[0].start()]
    preliminary = parse_metadata_block(preamble, False)
    is_v1 = "Format" in preliminary or "Schema Version" in preliminary
    if is_v1:
        if "Format" not in preliminary or preliminary.get("Format") != V1_FORMAT:
            fail("UGS-CR-014", "unsupported CR format")
        if "Schema Version" not in preliminary:
            fail("UGS-CR-015", "unsupported CR schema version")
        if "\r" in text:
            fail("UGS-CR-021", "v1 records must use LF line endings")
        if len(lines) < 2 or lines[1] != "":
            fail("UGS-CR-021", "v1 title must be followed by one blank line")
        if not preamble.startswith("\n") or not preamble.endswith("\n\n"):
            fail("UGS-CR-021", "v1 title must be followed by one blank line")
        metadata = parse_metadata_block(preamble[1:-2], True)
        sections = parse_sections(text, matches[0].start(), True)
        model = build_model(identifier, metadata, sections, V1_FORMAT, 1, True)
        if render(model) != text:
            fail("UGS-CR-021", "v1 record is not the canonical Markdown rendering")
        return model
    metadata = preliminary
    sections = parse_sections(text, matches[0].start(), False)
    return build_model(identifier, metadata, sections, LEGACY_FORMAT, 0, False)


def parse_path(path):
    file_path = Path(path)
    if not file_path.is_file():
        fail("UGS-CR-001", "record file does not exist: " + str(path))
    try:
        text = file_path.read_bytes().decode("utf-8")
    except UnicodeDecodeError as exc:
        fail("UGS-CR-017", "record must be UTF-8: " + str(exc))
    return parse_text(text)


def value_at(model, field):
    value = model
    for part in field.split("."):
        if not isinstance(value, dict) or part not in value:
            fail("UGS-CR-017", "unknown model field: " + field)
        value = value[part]
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--json", action="store_true", help="write the canonical JSON projection")
    mode.add_argument("--render", action="store_true", help="write canonical v1 Markdown")
    mode.add_argument("--field", help="write one scalar field using dotted notation")
    mode.add_argument("--coverage-oids", action="store_true", help="write coverage OIDs, one per line")
    parser.add_argument("record", help="CR Markdown record")
    args = parser.parse_args()
    try:
        model = parse_path(args.record)
        if args.render:
            sys.stdout.write(render(model))
        elif args.field:
            value = value_at(model, args.field)
            if value is None:
                return 0
            if isinstance(value, (dict, list)):
                sys.stdout.write(canonical_json(value) + "\n")
            else:
                sys.stdout.write(str(value) + "\n")
        elif args.coverage_oids:
            for oid in model["coverage_oids"]:
                print(oid)
        elif args.json:
            sys.stdout.write(json.dumps(model, ensure_ascii=False, indent=2, sort_keys=True) + "\n")
        return 0
    except CRModelError as exc:
        if os.environ.get("UGS_ERROR_FORMAT") == "json":
            sys.stderr.write(json.dumps({"format": "ugs-error/v1", "code": exc.code, "message": exc.message}, ensure_ascii=False, sort_keys=True) + "\n")
        else:
            sys.stderr.write(exc.code + ": " + exc.message + "\n")
        return 1


if __name__ == "__main__":
    sys.exit(main())
