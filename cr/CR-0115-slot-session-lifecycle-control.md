# CR-0115: Durable Slot Session Lifecycle Control

Base: main
Head or Range: 0518b96cdbed08177d8a3185a8cfc0afd7820d74
Integration Strategy: rebase-ff
Review Evidence: 0518b96cdbed08177d8a3185a8cfc0afd7820d74; go test ./...; go vet ./...; go test -race ./internal/store ./internal/coretransport; repository validators
Title: Durable slot session lifecycle control
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 18746c8aec2686e1ad27c53c858c829f89dacfa6
Head OID: 0518b96cdbed08177d8a3185a8cfc0afd7820d74
Integrated Result: pending

## Summary

Adds the second-wave Core control boundary for Windows execution-slot sessions.
Operations now carry phase, attempt, deadline, retry budget, desired state, and
revision metadata. Core and the local transport expose additive Cancel, Stop,
and Retry commands while older in-process SlotSessionAPI implementations remain
compatible.

The bbolt slot record persists the desired session intent and operation fence.
Controllers can claim work with an expiring owner lease and complete it only
when owner and generation fences match. Retry creates a new operation and
idempotency key; late or terminal results cannot revive an older attempt.

## Motivation

The previous boundary persisted only a Start request and projected whatever slot
status the reconciler last observed. It could select leased, draining, or
quarantined slots, had no durable stop/cancel intent, and could not distinguish
a crashed executor from a late completion. This change makes operation control
deterministic and keeps runtime facts separate from business account state.

## Test Evidence

- `go test ./...`
- `go vet ./...`
- `git diff --check`
- Store tests cover ineligible-slot selection, desired-state Stop and explicit
  revival, claim recovery, owner/generation checkpoint fencing, late completion
  fencing, retry idempotency/backoff, same-key payload conflicts, corruption
  validation, and terminal transition rejection.
- Core and transport tests cover redacted lifecycle projections and the three
  new wire methods.
- Native Windows login, service-host, installer, and managed-session acceptance
  remain pending under the production-session plan.

## Risk

The change is additive at the Core API and local transport layers. Existing
slot JSON records decode with zero values for the new intent fields. Runtime
assembly must consume the claim and desired-state APIs before production login
can be declared complete. No credentials, Profile paths, endpoints, or native
provider errors enter the new DTOs.

## Rollback

Revert this commit before enabling the lifecycle methods in service assembly.
Existing Start/Get callers and pre-change slot records remain readable.

## Breaking Change

No source-breaking change to `SlotSessionAPI`; lifecycle support is an additive
interface and advertised transport methods.

## Backport Target

None.
