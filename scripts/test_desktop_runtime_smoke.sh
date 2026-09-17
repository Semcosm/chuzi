#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <runtime-binary>" >&2
  exit 2
fi

runtime_binary="$1"
[ -x "$runtime_binary" ] || { echo "runtime binary is not executable: $runtime_binary" >&2; exit 1; }

profile_dir="$(mktemp -d)"
output_file="$(mktemp)"
error_file="$(mktemp)"
runtime_dir="$(mktemp -d)"
weston_log="$(mktemp)"
weston_pid=""
cleanup() {
  if [ -n "$weston_pid" ] && kill -0 "$weston_pid" 2>/dev/null; then
    kill "$weston_pid" 2>/dev/null || true
    wait "$weston_pid" 2>/dev/null || true
  fi
  rm -rf "$profile_dir" "$runtime_dir" "$output_file" "$error_file" "$weston_log"
}
trap cleanup EXIT

run_x11() {
  timeout 30s xvfb-run -a dbus-run-session -- env GDK_BACKEND=x11 WINIT_UNIX_BACKEND=x11 \
    WEBKIT_DISABLE_DMABUF_RENDERER=1 WEBKIT_DISABLE_COMPOSITING_MODE=1 \
    LIBGL_ALWAYS_SOFTWARE=1 "$runtime_binary"
}
printf '%s\n' \
  '{"protocol":"v1","id":"hello-x11","type":"hello"}' \
  "{\"protocol\":\"v1\",\"id\":\"start-x11\",\"type\":\"session_start\",\"payload\":{\"session_id\":\"session-x11\",\"account_id\":\"account-x11\",\"request_id\":\"request-x11\",\"profile_dir\":\"$profile_dir\"}}" \
  | run_x11 >"$output_file" 2>"$error_file" || {
    status=$?
    sed -n '1,160p' "$output_file" >&2
    sed -n '1,160p' "$error_file" >&2
    exit "$status"
  }
grep -Fq 'browser-runtime.linux-x11' "$output_file"
grep -Fq 'local-test-page-ready' "$output_file"

chmod 700 "$runtime_dir"
XDG_RUNTIME_DIR="$runtime_dir" weston \
  --backend=headless-backend.so --renderer=pixman --socket=wayland-1 --idle-time=0 \
  >"$weston_log" 2>&1 &
weston_pid=$!
for _ in $(seq 1 50); do
  if [ -S "$runtime_dir/wayland-1" ]; then
    break
  fi
  if ! kill -0 "$weston_pid" 2>/dev/null; then
    sed -n '1,160p' "$weston_log" >&2
    exit 1
  fi
  sleep 0.2
done
test -S "$runtime_dir/wayland-1"
printf '%s\n' \
  '{"protocol":"v1","id":"hello-wayland","type":"hello"}' \
  "{\"protocol\":\"v1\",\"id\":\"start-wayland\",\"type\":\"session_start\",\"payload\":{\"session_id\":\"session-wayland\",\"account_id\":\"account-wayland\",\"request_id\":\"request-wayland\",\"profile_dir\":\"$profile_dir\"}}" \
  | timeout 30s dbus-run-session -- env XDG_RUNTIME_DIR="$runtime_dir" WAYLAND_DISPLAY=wayland-1 \
      XDG_SESSION_TYPE=wayland GDK_BACKEND=wayland WINIT_UNIX_BACKEND=wayland \
      WEBKIT_DISABLE_DMABUF_RENDERER=1 WEBKIT_DISABLE_COMPOSITING_MODE=1 \
      LIBGL_ALWAYS_SOFTWARE=1 "$runtime_binary" >"$output_file" 2>"$error_file" || {
    status=$?
    sed -n '1,160p' "$output_file" >&2
    sed -n '1,160p' "$error_file" >&2
    sed -n '1,160p' "$weston_log" >&2
    exit "$status"
  }
grep -Fq 'browser-runtime.linux-wayland' "$output_file"
grep -Fq 'local-test-page-ready' "$output_file"

echo "desktop runtime X11 and Wayland smoke tests passed"
