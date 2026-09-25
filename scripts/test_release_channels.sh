#!/usr/bin/env bash
set -euo pipefail

repo_root="${BASH_SOURCE[0]}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
python3 - "$repo_root" <<'PY'
import json
import subprocess
import sys

root = sys.argv[1]
script = f"{root}/scripts/resolve_release_channel.py"
sha = "0123456789abcdef0123456789abcdef01234567"


def resolve(event, ref, channel=""):
    output = subprocess.check_output(
        [sys.executable, script, "--event", event, "--ref", ref,
         "--input-channel", channel, "--run-number", "42", "--sha", sha],
        text=True,
    )
    return json.loads(output)


def expect_failure(event, ref, channel):
    result = subprocess.run(
        [sys.executable, script, "--event", event, "--ref", ref,
         "--input-channel", channel, "--run-number", "42", "--sha", sha],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    assert result.returncode != 0


assert resolve("workflow_dispatch", "refs/heads/feature/x", "test") == {
    "channel": "test", "version": "test-42-0123456789ab"
}
assert resolve("workflow_dispatch", "refs/heads/main", "nightly") == {
    "channel": "nightly", "version": "nightly-42-0123456789ab"
}
assert resolve("schedule", "refs/heads/main") == {
    "channel": "nightly", "version": "nightly-42-0123456789ab"
}
assert resolve("push", "refs/heads/main") == {
    "channel": "ci", "version": "dev-0123456789ab"
}
assert resolve("push", "refs/tags/v1.2.3") == {
    "channel": "stable", "version": "v1.2.3"
}
expect_failure("workflow_dispatch", "refs/heads/feature/x", "nightly")
print("release channel contract passed")
PY
