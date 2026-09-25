#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <run-id> <release-tag> <commit-sha>" >&2
  exit 2
fi

run_id="$1"
tag="$2"
commit="${3,,}"
repo="${GH_REPOSITORY:-Semcosm/chuzi}"

fail() { echo "release retry validation failed: $1" >&2; exit 1; }
[[ "$run_id" =~ ^[0-9]+$ ]] || fail "run id must be numeric"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "release tag must match v<major>.<minor>.<patch>"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || fail "commit must be a full lowercase SHA-1"
command -v gh >/dev/null 2>&1 || fail "gh is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"

run_json="$(gh api "repos/$repo/actions/runs/$run_id")"
python3 - "$run_json" "$commit" <<'PY'
import json
import sys
run = json.loads(sys.argv[1])
commit = sys.argv[2]
if run.get("name") != "chuzi-build":
    raise SystemExit(f"run workflow must be chuzi-build, got {run.get('name')}")
if run.get("event") != "push":
    raise SystemExit(f"run event is not a build event: {run.get('event')}")
if not str(run.get("ref", "")).startswith("refs/tags/v"):
    raise SystemExit(f"run ref is not a stable release tag: {run.get('ref')}")
if run.get("status") != "completed" or run.get("conclusion") not in {"success", "failure"}:
    raise SystemExit(f"run is not a completed build: {run.get('status')}/{run.get('conclusion')}")
if str(run.get("head_sha", "")).lower() != commit:
    raise SystemExit(f"run head SHA {run.get('head_sha')} does not match {commit}")
PY

jobs_json="$(gh api "repos/$repo/actions/runs/$run_id/jobs?per_page=100")"
python3 - "$jobs_json" "$commit" <<'PY'
import json
import sys
jobs = json.loads(sys.argv[1]).get("jobs", [])
commit = sys.argv[2]
expected = {"chuzi-build-windows-amd64", "chuzi-build-linux-amd64", "chuzi-build-linux-arm64", "chuzi-build-darwin-arm64", "chuzi-build"}
selected = {job.get("name"): job for job in jobs if job.get("name") in expected}
if set(selected) != expected:
    raise SystemExit(f"run is missing build jobs: {sorted(expected - set(selected))}")
for name, job in selected.items():
    if job.get("status") != "completed" or job.get("conclusion") != "success":
        raise SystemExit(f"job {name} is not successful: {job.get('status')}/{job.get('conclusion')}")
    if str(job.get("head_sha", "")).lower() != commit:
        raise SystemExit(f"job {name} head SHA does not match run")
unexpected_failures = [
    job.get("name") for job in jobs
    if job.get("name") not in expected
    and job.get("name") != "stable-release"
    and job.get("conclusion") not in {"success", "skipped", "neutral"}
]
if unexpected_failures:
    raise SystemExit(f"run contains unexpected failed jobs: {unexpected_failures}")
PY

artifacts_json="$(gh api "repos/$repo/actions/runs/$run_id/artifacts?per_page=100")"
python3 - "$artifacts_json" <<'PY'
import json
import re
import sys
artifacts = json.loads(sys.argv[1]).get("artifacts", [])
expected = {f"chuzi-stable-{target}" for target in ("windows-amd64", "linux-amd64", "linux-arm64", "darwin-arm64")}
selected = {item.get("name"): item for item in artifacts if item.get("name") in expected}
if set(selected) != expected:
    raise SystemExit(f"missing target artifacts: {sorted(expected - set(selected))}")
for name, item in selected.items():
    if item.get("expired") or not isinstance(item.get("size_in_bytes"), int) or item["size_in_bytes"] <= 0:
        raise SystemExit(f"artifact is empty or expired: {name}")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", str(item.get("digest", ""))):
        raise SystemExit(f"artifact digest is missing or invalid: {name}")
PY

printf '%s\n' "release retry inputs validated: run=$run_id tag=$tag commit=$commit"
