#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 <run-id> [expected-commit]" >&2
  echo "downloads and validates one successful workflow_dispatch nightly run" >&2
  exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
run_id="$1"
expected_commit="${2:-$(git rev-parse HEAD)}"
[[ "$run_id" =~ ^[0-9]+$ ]] || { echo "run id must be numeric" >&2; exit 2; }
[[ "$expected_commit" =~ ^[0-9a-fA-F]{40}$ ]] || { echo "expected commit must be a full SHA-1" >&2; exit 2; }
expected_commit="$(printf '%s' "$expected_commit" | tr '[:upper:]' '[:lower:]')"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo="${GH_REPOSITORY:-Semcosm/chuzi}"
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || { echo "GH_REPOSITORY must be owner/repository" >&2; exit 2; }
download_root="$(mktemp -d)"
trap 'rm -rf "$download_root"' EXIT

command -v gh >/dev/null 2>&1 || { echo "nightly acceptance requires gh" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "nightly acceptance requires python3" >&2; exit 1; }

run_json="$(gh api "repos/$repo/actions/runs/$run_id")"
run_info="$(python3 - "$run_json" "$expected_commit" <<'PY'
import json
import sys
run = json.loads(sys.argv[1])
expected = sys.argv[2].lower()
if run.get("name") != "chuzi-build":
    raise SystemExit(f"run workflow must be chuzi-build, got {run.get('name')}")
if run.get("event") != "workflow_dispatch":
    raise SystemExit(f"run event must be workflow_dispatch, got {run.get('event')}")
if run.get("status") != "completed" or run.get("conclusion") != "success":
    raise SystemExit(f"run is not successful: {run.get('status')}/{run.get('conclusion')}")
if run.get("head_branch") != "main":
    raise SystemExit(f"run branch must be main, got {run.get('head_branch')}")
if str(run.get("head_sha", "")).lower() != expected:
    raise SystemExit(f"run head SHA {run.get('head_sha')} does not match expected {expected}")
if not isinstance(run.get("run_number"), int) or run["run_number"] <= 0:
    raise SystemExit("run number is invalid")
print(json.dumps({"run_number": run["run_number"], "head_sha": run["head_sha"], "version": f"nightly-{run['run_number']}-{expected[:12]}"}, sort_keys=True))
PY
)"
printf '%s\n' "$run_info"

jobs_json="$(gh api "repos/$repo/actions/runs/$run_id/jobs?per_page=100")"
python3 - "$jobs_json" "$expected_commit" <<'PY'
import json
import sys
jobs = json.loads(sys.argv[1]).get("jobs", [])
expected_commit = sys.argv[2].lower()
expected = {"chuzi-build-windows-amd64", "chuzi-build-linux-amd64", "chuzi-build-linux-arm64", "chuzi-build-darwin-arm64", "chuzi-build"}
found = {job.get("name") for job in jobs if job.get("name") in expected}
if found != expected:
    raise SystemExit(f"run is missing build jobs: {sorted(expected - found)}")
for job in jobs:
    if job.get("name") in expected and (job.get("status") != "completed" or job.get("conclusion") != "success"):
        raise SystemExit(f"job {job.get('name')} is not successful: {job.get('status')}/{job.get('conclusion')}")
    if job.get("name") in expected and str(job.get("head_sha", "")).lower() != expected_commit:
        raise SystemExit(f"job {job.get('name')} head SHA does not match run")
PY

artifacts_json="$(gh api "repos/$repo/actions/runs/$run_id/artifacts?per_page=100")"
python3 - "$artifacts_json" "$run_info" <<'PY'
import datetime
import json
import re
import sys
artifacts = json.loads(sys.argv[1]).get("artifacts", [])
run = json.loads(sys.argv[2])
expected = {f"chuzi-nightly-{target}" for target in ("windows-amd64", "linux-amd64", "linux-arm64", "darwin-arm64")}
selected = {}
for artifact in artifacts:
    name = artifact.get("name")
    if name in expected:
        if name in selected:
            raise SystemExit(f"duplicate Actions artifact: {name}")
        if artifact.get("expired") or not isinstance(artifact.get("size_in_bytes"), int) or artifact["size_in_bytes"] <= 0:
            raise SystemExit(f"artifact is empty or expired: {name}")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", str(artifact.get("digest", ""))):
            raise SystemExit(f"artifact digest is missing or invalid: {name}")
        expires = artifact.get("expires_at")
        if not expires:
            raise SystemExit(f"artifact retention metadata is missing: {name}")
        expiry = datetime.datetime.fromisoformat(expires.replace("Z", "+00:00"))
        if expiry <= datetime.datetime.now(datetime.timezone.utc):
            raise SystemExit(f"artifact has expired: {name}")
        selected[name] = {"id": artifact.get("id"), "digest": artifact["digest"], "size": artifact["size_in_bytes"], "expires_at": expires}
if set(selected) != expected:
    raise SystemExit(f"missing nightly artifacts: {sorted(expected - set(selected))}")
print(json.dumps({"run": run, "artifacts": selected}, sort_keys=True))
PY

expected_version="$(printf '%s' "$run_info" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')"

for target in windows-amd64 linux-amd64 linux-arm64 darwin-arm64; do
  name="chuzi-nightly-$target"
  target_dir="$download_root/$target"
  mkdir -p "$target_dir"
  echo "[nightly-acceptance] downloading $name" >&2
  gh run download "$run_id" --repo "$repo" --name "$name" --dir "$target_dir"
  index_path="$target_dir/chuzi-${expected_version}-${target}.index.json"
  python3 "$repo_root/scripts/validate_release_index.py" --index "$index_path" --dist "$target_dir"
  "$repo_root/scripts/validate_nightly_artifact.py" --artifact "$target_dir" --target "$target" --commit "$expected_commit" --version "$expected_version"
done

host_target=""
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) host_target=linux-amd64 ;;
  Linux/aarch64|Linux/arm64) host_target=linux-arm64 ;;
  Darwin/arm64) host_target=darwin-arm64 ;;
  MINGW*/x86_64|MSYS*/x86_64|CYGWIN*/x86_64) host_target=windows-amd64 ;;
esac
if [ -n "$host_target" ]; then
  install_root="$download_root/host-install"
  mkdir -p "$install_root"
  "$repo_root/scripts/validate_nightly_artifact.py" --artifact "$download_root/$host_target" --target "$host_target" --commit "$expected_commit" --version "$expected_version" --extract-launcher "$install_root" >/dev/null
  launcher_binary="$install_root/chuzi-launcher"
  [ "$host_target" = "windows-amd64" ] && launcher_binary="$install_root/chuzi-launcher.exe"
  server_port_file="$download_root/server-port"
  server_log="$download_root/server.log"
  python3 - "$download_root/$host_target" "$server_port_file" >"$server_log" 2>&1 <<'PY' &
import functools
import pathlib
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

root = pathlib.Path(sys.argv[1]).resolve()
port_file = pathlib.Path(sys.argv[2])
handler = functools.partial(SimpleHTTPRequestHandler, directory=str(root))
server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
port_file.write_text(str(server.server_address[1]), encoding="ascii")
server.serve_forever()
PY
  server_pid=$!
  cleanup_server() {
    if [ -n "${server_pid:-}" ] && kill -0 "$server_pid" 2>/dev/null; then
      kill "$server_pid" 2>/dev/null || true
      wait "$server_pid" 2>/dev/null || true
    fi
  }
  trap 'cleanup_server; rm -rf "$download_root"' EXIT
  while [ ! -s "$server_port_file" ]; do
    if ! kill -0 "$server_pid" 2>/dev/null; then
      sed -n '1,120p' "$server_log" >&2
      exit 1
    fi
    sleep 0.1
  done
  server_port="$(cat "$server_port_file")"
  mkdir -p "$download_root/launcher-tmp"
  launcher_args=(-root "$install_root" -manifest "$install_root/release-manifest.json" -release-index "http://127.0.0.1:$server_port/chuzi-${expected_version}-${host_target}.index.json" -allow-http-loopback -progress)
  run_launcher() {
    phase="$1"
    shift
    launcher_stdout="$download_root/launcher-$phase.stdout"
    launcher_stderr="$download_root/launcher-$phase.stderr"
    echo "[nightly-acceptance] launcher phase $phase" >&2
    if env TMPDIR="$download_root/launcher-tmp" TMP="$download_root/launcher-tmp" TEMP="$download_root/launcher-tmp" "$launcher_binary" "$@" >"$launcher_stdout" 2>"$launcher_stderr"; then
      cat "$launcher_stderr" >&2
      cat "$launcher_stdout"
    else
      status=$?
      echo "launcher phase $phase failed with status $status" >&2
      sed -n '1,160p' "$launcher_stdout" >&2
      sed -n '1,160p' "$launcher_stderr" >&2
      return "$status"
    fi
  }
  run_launcher version -version
  test "$(tr -d '\r\n' <"$launcher_stdout")" = "$expected_version"
  run_launcher initialize "${launcher_args[@]}" -command initialize >/dev/null
  python3 - "$launcher_stdout" <<'PY'
import json
import sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("first_run") is not True or data.get("next_action") != "select_components" or "launcher" not in data.get("required", []):
    raise SystemExit(f"unexpected initialize result: {data}")
PY
  run_launcher component-install "${launcher_args[@]}" -command component-install -item service >/dev/null
  python3 - "$launcher_stdout" "$expected_version" <<'PY'
import json
import sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("id") != "service" or data.get("installed") is not True or data.get("version") != sys.argv[2] or data.get("health") != "healthy":
    raise SystemExit(f"unexpected component-install result: {data}")
PY
  run_launcher component-list "${launcher_args[@]}" -command component-list >/dev/null
  python3 - "$launcher_stdout" "$expected_version" <<'PY'
import json
import sys
states = {item.get("id"): item for item in json.load(open(sys.argv[1], encoding="utf-8"))}
for component in ("launcher", "browser-worker", "service"):
    state = states.get(component)
    if not state or state.get("installed") is not True or state.get("version") != sys.argv[2] or state.get("health") != "healthy":
        raise SystemExit(f"unexpected component-list result for {component}: {state}")
PY
  run_launcher initialize-complete "${launcher_args[@]}" -command initialize-complete >/dev/null
  python3 - "$launcher_stdout" <<'PY'
import json
import sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data != {"initialized": True}:
    raise SystemExit(f"unexpected initialize-complete result: {data}")
PY
  cleanup_server
fi

echo "nightly consumer acceptance passed for run $run_id ($expected_commit)" >&2
