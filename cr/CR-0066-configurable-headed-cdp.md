# CR-0066: add configurable headed and headless CDP runtime

Base: main
Head or Range: feat/windows-ui-client-foundation
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(browser): add configurable headed and headless CDP runtime
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Head OID: 2a69ed603dd8acbb2a61af16f25999f7b7b0946f
Integrated Result: pending

## Summary

Make the Node browser worker selectable between headed-CDP and headless-CDP
runtime modes while preserving the deferred worker backend. The service now
defaults to headed mode, accepts a shared browser command configuration, and
passes runtime selection through the Core and automation adapter boundaries.
On Windows, headed launches can use a small native helper that creates the
browser process on a named desktop in the current interactive session and
reaps it through a Job Object. The helper is included in Windows builds and
release packages.

## Motivation

The first implementation increment needs visible browser windows for local
operation while retaining an explicit headless option for deployments without
a visible desktop. Windows operators also need to place a headed browser on a
specific desktop without creating a second logon session or weakening the
existing loopback-only CDP boundary.

## Test Evidence

`GOTMPDIR=/home/chen/go-tmp-chuzi GOCACHE=/home/chen/.cache/go-build-chuzi go test ./...`

`GOTMPDIR=/home/chen/go-tmp-chuzi GOCACHE=/home/chen/.cache/go-build-chuzi go vet ./...`

`npm test --prefix browser-worker` (18 Node tests)

`./scripts/validate_policy_manifest.sh`

`./scripts/validate_quality_profile.sh`

`./scripts/validate_supply_chain_profile.sh`

`./scripts/validate_repository_shape.sh`

`./scripts/test_build_contract.sh`

`git diff --check`

Windows amd64 cross-compilation for the service, launcher, and browser helper
was completed before this commit. A real Windows desktop/session launch was
not available in the current environment.

## Risk

Headed mode requires a graphical browser executable and, when configured,
access to the named desktop in the current Windows session. The helper does
not create desktops or bypass Windows session permissions; an unavailable or
inaccessible desktop fails closed. Browser commands remain explicit argv
lists, CDP stays on loopback with a dynamic port, Profiles remain service-
generated, and browser output is not sent into the worker protocol.

The default helper command is the packaged `chuzi-browser-launcher.exe` on
Windows. Deployments using a non-standard installation layout can override
it with `-windows-launcher-command`.

## Rollback

Revert commit `e44b53048b566f4a8722bf32b353aa7d44ccd73c` and this CR. The
prior Node deferred and explicit headless worker paths remain compatible; the
Windows browser helper is additive and can be omitted from older packages.

## Breaking Change

The default service browser backend changes from the Node deferred worker to
headed-CDP. Deployments that cannot provide a visible browser should set
`-browser-backend headless` or `-browser-backend node`. The prior
`-headless-browser-command` flag remains as a compatibility alias for the new
`-browser-command` option.

## Backport Target

none
