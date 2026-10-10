# CR-0109: start and refresh execution slot sessions in Settings

Base: main
Head or Range: 00d33a52a71839b6eca98eac55ce79f3054ed7da
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(ui): start and refresh execution slot sessions
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 9d8de9352a2a14429a6b1039bd91f26683a94ff1
Head OID: 00d33a52a71839b6eca98eac55ce79f3054ed7da
Integrated Result: pending

## Summary

Add Start basic session to Settings pool cards using the existing confirmation,
launcher core-call, background worker, and busy guard. Submit the pool ID, actor,
current config revision, and deterministic idempotency key; Core chooses the
execution slot. Poll get_slot_session_operation and show redacted readiness,
agent state, environment generation, stable failure classes, and idempotency.

Place the operation panel above pool cards and retain manual refresh after ready
or failed results. Preserve the idempotent marker for the same operation and
keep refresh routing unchanged when another action is rejected by the busy guard.
Whitelist slot state and failure strings before rendering them. Update Core API
and UI contracts plus deterministic layout fixtures.

## Motivation

The durable Core slot-session methods needed an operator-facing control and
queryable readiness projection in the native client. Execution slot sessions
remain separate from the business request Sessions list.

## Test Evidence

Passed: go test ./...; go vet ./...; cargo test --manifest-path
ui/windows/Cargo.toml --locked (44 tests); cargo fmt --manifest-path
ui/windows/Cargo.toml -- --check; validate_policy_manifest.sh,
validate_quality_profile.sh, validate_supply_chain_profile.sh,
validate_action_pinning.sh, validate_repository_shape.sh, test_build_contract.sh;
and git diff --check.

The Slint testing backend and temporary launcher protocol fixture verify the
confirmation callback, requested/provisioning/ready polling, busy rejection,
idempotent retries, refresh routing, failed results, stale revision, unavailable
slot recovery, and an unchanged business Sessions list. A separate test verifies
unknown-state and failure redaction plus idempotency reset for a new operation.
The launcher fixture runs on Unix and creates no native Windows sessions.

Generated 36 dimension/non-blank layout snapshots: mixed plus five slot-session
fixtures, each at 800x600, 1120x760, and 1440x900 in light and dark themes.
Inspected the compact ready snapshot to confirm visible readiness and refresh.
This is not a pixel-level golden comparison. Generated images remain under
ignored dist/. The conditional document-map validator is not applicable because
.ugs/document-map.json is absent.

Native Windows WTS session acceptance remains deployment-owned and was not
performed on the Linux development host. Remote four-target build validation
will run on this topic branch with the test channel.

## Risk

The client relies on the existing Core operation and revision contracts. Unknown
slot status values render as unknown and unknown failure codes render as a stable
session_failed classification. Busy ownership prevents overlapping UI actions.
This change adds no RDP capability handoff, credentials, Windows provider, browser
automation, auto-login, or production data access.

## Rollback

Revert the UI implementation commit and associated documentation. Core methods
and stored slot-session operations remain unchanged and queryable.

## Breaking Change

None. Older Core versions may report unavailable for the additive slot-session
methods; the UI presents a bounded recovery message.

## Backport Target

None
