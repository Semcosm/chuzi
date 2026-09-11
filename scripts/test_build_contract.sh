#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
fail() { echo "build contract validation failed: $1" >&2; exit 1; }

for target in windows-amd64 linux-amd64 linux-arm64 darwin-arm64; do
  grep -Fq "$target" "$repo_root/scripts/build.sh" || fail "build.sh misses $target"
  grep -Fq "$target" "$repo_root/scripts/build.ps1" || fail "build.ps1 misses $target"
  grep -Fq "$target" "$repo_root/scripts/package.ps1" || fail "package.ps1 misses $target"
done

[ -f "$repo_root/go.mod" ] || fail "go.mod is missing"
[ -f "$repo_root/browser-worker/package.json" ] || fail "browser worker package manifest is missing"
[ -f "$repo_root/browser-worker/package-lock.json" ] || fail "browser worker lockfile is missing"
[ -f "$repo_root/browser-worker/src/headless.mjs" ] || fail "headless worker is missing"
[ -f "$repo_root/browser-runtime/Cargo.toml" ] || fail "browser runtime Cargo manifest is missing"
[ -f "$repo_root/browser-runtime/Cargo.lock" ] || fail "browser runtime Cargo lockfile is missing"
[ -f "$repo_root/.github/workflows/chuzi-build.yml" ] || fail "build workflow is missing"

grep -Fq 'name: chuzi-build' "$repo_root/.github/workflows/chuzi-build.yml" || fail "aggregate build check is missing"
grep -Eq 'actions/checkout@[0-9a-f]{40}' "$repo_root/.github/workflows/chuzi-build.yml" || fail "workflow action is not pinned"
grep -Fq 'cargo test --locked --manifest-path browser-runtime/Cargo.toml' "$repo_root/.github/workflows/chuzi-build.yml" || fail "workflow misses browser runtime tests"
grep -Fq 'chuzi-browser-runtime' "$repo_root/scripts/build.sh" || fail "build.sh misses Rust helper"
grep -Fq 'chuzi-browser-runtime' "$repo_root/scripts/build.ps1" || fail "build.ps1 misses Rust helper"
grep -Fq 'browserRuntime' "$repo_root/scripts/build.sh" || fail "build.sh manifest misses runtime backend"
grep -Fq 'browserRuntime' "$repo_root/scripts/build.ps1" || fail "build.ps1 manifest misses runtime backend"
grep -Fq 'desktop-webview' "$repo_root/scripts/build.sh" || fail "build.sh misses desktop feature"
grep -Fq 'desktop-webview' "$repo_root/scripts/build.ps1" || fail "build.ps1 misses desktop feature"
grep -Fq 'Test native desktop WebView helper contract' "$repo_root/.github/workflows/chuzi-build.yml" || fail "workflow misses desktop helper contract"
grep -Fq 'linux-amd64) goos=linux; goarch=amd64; binary=chuzi; runtime_backend=wry-desktop; runtime_features=desktop-webview' "$repo_root/scripts/build.sh" || fail "build.sh keeps Linux amd64 deferred"
grep -Fq 'linux-arm64) goos=linux; goarch=arm64; binary=chuzi; runtime_backend=wry-desktop; runtime_features=desktop-webview' "$repo_root/scripts/build.sh" || fail "build.sh keeps Linux arm64 deferred"
grep -Fq 'Smoke test Linux WebKitGTK on X11' "$repo_root/.github/workflows/chuzi-build.yml" || fail "workflow misses Linux X11 smoke test"
grep -Fq 'Smoke test Linux WebKitGTK on Wayland' "$repo_root/.github/workflows/chuzi-build.yml" || fail "workflow misses Linux Wayland smoke test"
grep -Fq 'headless-browser-command' "$repo_root/cmd/service/main.go" || fail "service misses headless browser command option"
grep -Fq 'browser-runtime.headless-cdp' "$repo_root/browser-worker/src/headless.mjs" || fail "headless worker capability is missing"

echo "build contract validation passed"
