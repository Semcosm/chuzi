#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then echo "usage: $0 <run-id> <full-commit-sha>" >&2; exit 2; fi
run_id="$1"
expected_commit="$(printf "%s" "$2" | tr "[:upper:]" "[:lower:]")"
[[ "$run_id" =~ ^[0-9]+$ ]] || { echo "run id must be numeric" >&2; exit 2; }
[[ "$expected_commit" =~ ^[0-9a-f]{40}$ ]] || { echo "commit must be a full SHA-1" >&2; exit 2; }
repo="${GH_REPOSITORY:-Semcosm/chuzi}"
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
download_root="$(mktemp -d)"
trap "rm -rf $download_root" EXIT
command -v gh >/dev/null 2>&1 || { echo "test acceptance requires gh" >&2; exit 1; }
run_json="$(gh api "repos/$repo/actions/runs/$run_id")"
run_number="$(python3 -c "import json,sys; r=json.loads(sys.argv[1]); e=sys.argv[2]; assert r.get(\"name\") == \"chuzi-build\" and r.get(\"event\") == \"workflow_dispatch\"; assert r.get(\"status\") == \"completed\" and r.get(\"conclusion\") == \"success\"; assert r.get(\"head_sha\",\"\").lower() == e; print(r[\"run_number\"] )" "$run_json" "$expected_commit")"
expected_version="test-$run_number-$(printf "%s" "$expected_commit" | cut -c1-12)"
artifacts_json="$(gh api "repos/$repo/actions/runs/$run_id/artifacts?per_page=100")"
python3 -c "import json,sys; a=json.loads(sys.argv[1]).get(\"artifacts\",[]); e={\"chuzi-test-\"+t for t in (\"windows-amd64\",\"linux-amd64\",\"linux-arm64\",\"darwin-arm64\")}; f={x.get(\"name\") for x in a}; assert e <= f, sorted(e-f)" "$artifacts_json"
for target in windows-amd64 linux-amd64 linux-arm64 darwin-arm64; do
  name="chuzi-test-$target"; target_dir="$download_root/$target"; mkdir -p "$target_dir"
  gh run download "$run_id" --repo "$repo" --name "$name" --dir "$target_dir"
  index="$target_dir/chuzi-$expected_version-$target.index.json"
  python3 "$repo_root/scripts/validate_release_index.py" --index "$index" --dist "$target_dir"
  "$repo_root/scripts/validate_nightly_artifact.py" --artifact "$target_dir" --target "$target" --commit "$expected_commit" --version "$expected_version" --channel test
done
echo "test consumer acceptance passed for run $run_id ($expected_commit)" >&2
