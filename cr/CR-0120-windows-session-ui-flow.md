# CR-0120: complete Windows UI slot-session operation flow

Base: main
Head or Range: fcee169c2d795dd33974a6ba73d111c6f69af899
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows UI slot-session operation flow
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: fcee169c2d795dd33974a6ba73d111c6f69af899
Integrated Result: pending

## Summary

Complete the Session 08 Windows Rust/Slint session operation flow on top of the
Session 07 Core runtime contract. The Settings pool cards now submit the real
Core StartSlotSession operation, poll its durable projection, and expose
confirmed Cancel, Stop, and Retry actions. The UI keeps transport retries on
the same idempotency key while every new user intent receives a new bounded
key, persists a redacted active-operation pointer in the interactive user's
state directory, and resumes the operation by ID after UI restart.

The projection maps only allow-listed slot ordinal, phase, attempt, deadline,
generation, revision, readiness, and stable failure classes. Start is gated by
known mode/revision, approved environment readiness, pool state, and active
operation ownership. Polling uses bounded backoff and a local generation fence;
late responses cannot overwrite a newer operation or selection. The offline
launcher fixture and layout snapshots cover preflight gating, transport retry,
action wire calls, cleanup terminal states, stale revision, unavailable slots,
redaction, and restart resume.

## Motivation

The previous UI submitted only a basic start request, reused a revision-derived
key for later clicks, and treated polling cancellation as a local dialog state.
That could show a false idle state while Core still owned the durable operation,
or repeat an old intent after a revision change. Session 08 makes the UI a
projection and action client of the Session 07 Core operation contract without
reading storage, credentials, Profiles, pipes, or native session state.

## Test Evidence

- cargo fmt --manifest-path ui/windows/Cargo.toml -- --check passed.
- In the Windows MSVC Developer Command Prompt with VCPKG_ROOT set to the
  locally installed vcpkg and VCPKGRS_TRIPLET=x64-windows-static,
  cargo test --manifest-path ui/windows/Cargo.toml --locked passed: 51 tests.
- The same environment ran cargo nextest run --manifest-path
  ui/windows/Cargo.toml --locked: 51 passed, 0 skipped. The Unix-only Python
  process fixtures remain covered by their guarded test path; this run validates
  the Windows build and pure state/projection tests.
- cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot
  --locked --example layout_snapshot -- --session-state slot-session-ready
  --theme both --output dist/ui-sessions passed. The complete Session 08
  matrix produced 48 ignored images across 800x600, 1120x760, and 1440x900
  Light/Dark cases; the probe checks dimensions and non-blank output, not
  pixel-golden equality.
- go test -count=1 ./..., go test -count=1 -race ./..., and go vet ./... passed.
- Windows amd64 test compilation passed for cmd/service, cmd/session-broker,
  and internal/slotwindows.
- validate_supply_chain_profile.sh, validate_action_pinning.sh,
  validate_repository_shape.sh, test_build_contract.sh, and git diff --check
  passed. The policy and quality validators also passed after normalizing the
  CRLF emitted by the Windows jq executable; their direct Git Bash run sees
  README.md\r as a filename and is a toolchain newline issue.
- The local FreeRDP build prerequisite was installed with vcpkg
  (freerdp[client]:x64-windows-static) and the Rust crate compiled against
  the MSVC static triplet.

## Risk

The UI can leave a durable operation visible as still running when its bounded
local polling window expires or Core restarts; the operation ID remains
available for explicit refresh and the active pointer is not cleared until a
terminal Core projection is received. The UI does not perform native WTS, RDP,
Profile, Credential Manager, ACL, Agent, worker, restart, shutdown, or Retire
actions itself. Fixture tests use synthetic redacted data and no live account
or credential.

## Rollback

Revert implementation commit fcee169c2d795dd33974a6ba73d111c6f69af899 and
the CR-0120 record commit, then rerun the UI and repository checks. Session 07
Core runtime commits remain independent.

## Breaking Change

None to the Core wire contract. The UI now requires the Session 07 slot-session
methods and keeps Start disabled until their redacted readiness gates are
known.

## Backport Target

None.

## Native Pending

Session 09/10 must perform Windows UI Automation and real installed-host
acceptance. WTS token/query and logoff, RDP/Credential Manager, Profile
creation, pipe and service ACLs, Agent/worker readiness, broker restart,
shutdown, and destructive Retire behavior remain pending and are not claimed
by this record. Linux tests, Windows cross-compilation, and synthetic layout or
launcher fixtures are not native acceptance.
