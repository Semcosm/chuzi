# CR-0011: implement authorized Matrix adapter and durable status notifications

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(matrix): add authorized command adapter and durable notifications
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3a9f03f915db5b9bf8cae67c3d98ab34f7be9e28
Head OID: 3a9f03f915db5b9bf8cae67c3d98ab34f7be9e28
Integrated Result: pending

## Summary

Implement the stage-six transport-neutral Matrix boundary. Add explicit room/user
authorization, fixed `status`, `request`, `cancel`, and `help` command parsing,
room-scoped request visibility, stable event-derived request/reply IDs, and
redacted operational events. Persist state notifications in a schema v4 bbolt
outbox; claim, retry, and recover delivery through an injected Sender without
persisting rendered message bodies or connecting to a production Matrix service.

## Motivation

The request service, queue, Session Runner, and encrypted Credential Store now
exist, but no user-facing adapter can submit or observe a request. Matrix must
remain below the domain state machine and credential boundary while surviving
duplicate events, disconnects, process restarts, and concurrent delivery
workers. A durable outbox generated in the same transaction as each account
state event keeps notification delivery from becoming a second business state
source.

## Test Evidence

Tests cover exact command grammar, default-deny room/user authorization,
room-scoped status/cancel access, administrator cross-room access, duplicate
Matrix event idempotency, stable reply/request IDs, transactional notification
creation, schema v3-to-v4 migration, restart recovery, claim ownership and
expiry, classified failure rendering, disconnect retry, event-ID delivery
deduplication, and redacted observability events. CI must run `gofmt`,
`go test ./...`, `go test -race ./...`, `go vet ./...`, repository validators,
and the four-target build/self-test matrix. The local restricted shell may not
have Go or Node toolchains, so remote CI remains authoritative for application
tests and builds.

## Risk

The schema v4 outbox and request notification-room field become durable
delivery contracts. A notification is created only for a request with an
authorized room target; missing targets preserve existing non-Matrix request
behavior. Claims expire and retries retain the domain event ID, so a sender can
deduplicate an acknowledgement race. Matrix replies and notifications expose
only a stable account hash, request ID, state, and classified failure; raw room
IDs, command text, credentials, and internal errors stay out of messages and
observability records. No Matrix SDK, access token, browser, or external
network is introduced.

## Rollback

Stop the service and preserve the bbolt database before reverting via a
subsequent CR. A schema v4 database must be opened with a binary that knows the
`matrix_notifications` bucket; undelivered records remain recoverable until
their configured retention policy removes them. No production Matrix sender
is enabled by this change.

## Breaking Change

The bbolt schema advances to v4 and adds `matrix_notifications`; v1-v3
databases upgrade repeatably. Request JSON gains an optional notification room
field, preserving existing callers that do not configure Matrix delivery. A
new internal Matrix and observability API is transport-neutral and does not
change the service entry point or worker protocol.

## Backport Target

none
