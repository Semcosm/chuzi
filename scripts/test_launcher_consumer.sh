#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

go test -count=1 -run '^TestLauncherConsumerFlow$' -v ./cmd/launcher
echo "launcher consumer acceptance passed"
