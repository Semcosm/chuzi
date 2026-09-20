# CR-0062: close the Windows Core channel and lifecycle loop

Base: main
Head or Range: feat/windows-core-release-loop
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): close Core channel and lifecycle loop
Revision: 21
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: deaaeefdfffdebfc4449efb61551003c39e2063d
Head OID: deaaeefdfffdebfc4449efb61551003c39e2063d
Integrated Result: main@deaaeefdfffdebfc4449efb61551003c39e2063d

## Summary

Exercise the Windows UI's real Core lifecycle boundary in CI, including
component installation, startup, named-pipe API calls, stop, restart, replace,
and uninstall. Keep the Settings page as the user-facing entry point for the
test/stable Core catalogs and publish test releases from untagged workflow
dispatches before stable annotated tags.

## Motivation

The existing Windows build proved that the Core executable and UI could start,
but it did not prove that the UI's launcher/controller path could complete an
installation or remove it. That left failures such as `pipe hasn't been
connected yet` and stale Core files visible only on a user's machine. A
controller-level smoke test must be authoritative before publishing a new
test-channel release, and the same release catalog must later expose the
stable tag without a second UI implementation.

The launcher and managed Core log readers now drain redirected process output
with synchronous reads on worker threads. This avoids the Windows anonymous
pipe race that could surface the same `pipe hasn't been connected yet` error
before a Core lifecycle command reached the service.

Startup status probes now skip pipe access when Core is not installed and use
the cancellable overlapped connection path when Core is stopped. This avoids
disposing a synchronous native connect while it is still running, which could
terminate an unpackaged WinUI process before its first window appeared.

## Test Evidence

Local checks:

`go test ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`./scripts/test_release_catalog.sh`

`./scripts/validate_repository_shape.sh`

`git diff --check`

Windows CI additionally builds `CoreLifecycleSmoke.csproj` and runs the
install/start/API/stop/restart/replace/uninstall sequence against the exact
payload shipped in the installer.

The installer smoke now copies `Program Files\\Chuzi\\CorePayload` into the
lifecycle harness and repeats the same controller sequence, proving that the
payload actually installed by `ChuziSetup.exe` is usable. Named-pipe request
handoffs wait 250 ms and retry up to eight fresh connections on slower Windows
hosts. User-facing lifecycle failures include the resolved data directory and
Core log path so stale installations can be diagnosed without guessing.

The installed-client smoke step also captures CoreHost tracing, the installed
payload file list, and the recent Application/AppModel Runtime events when
startup exits early.

Normal account and task operations now instantiate the Core client without a
readiness connection. Each operation uses one short-lived named-pipe
connection for the handshake and request, so the UI never creates a readiness
connection and immediately hands it off to a second request connection. This
removes the remaining Windows race behind `pipe hasn't been connected yet` from
the user-facing operation path. The lifecycle smoke covers this direct
operation path. Readiness probes now use the same short-lived handshake and
wait briefly for the server-side EOF before returning; the pipe smoke repeats
the probe-then-request sequence to cover the handoff that previously failed on
some Windows hosts.

The installed client is now a Rust + Slint executable with a headless layout
probe covering 800x600, 1120x760, and 1440x900 viewports. The hosted Windows
runner still has no interactive desktop, so the installed-client smoke checks
the packaged process and Windows crash records while the Core lifecycle and
named-pipe smokes remain authoritative in CI. The Slint client keeps theme
state UI-local and supports system, light, and dark palette schemes.

## Risk

The new smoke harness is test-only and does not alter the Core API or persisted
data format. Release catalog publication remains an append/update operation on
the dedicated catalog branch; stable publication still requires an annotated
semver tag and signed release inputs.

## Rollback

Revert the implementation commit. Existing Core manifests, component archives,
and the named-pipe protocol remain compatible.

## Breaking Change

No.

## Backport Target

none
