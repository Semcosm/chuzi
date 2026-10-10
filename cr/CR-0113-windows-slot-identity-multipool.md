# CR-0113: isolate Windows slot identities across pools

Base: main
Head or Range: 32727067eda9c9614bf006ad25500569a3fc2cd8
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): isolate slot identities across pools
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 1705e425d1234b355079a3c6bec539dd4b22cf46
Head OID: 32727067eda9c9614bf006ad25500569a3fc2cd8
Integrated Result: pending

## Summary

Replace the ordinal-only Windows local-user mapping with an installation-scoped,
slot-stable mapping shared by `slotwindows.Options` and future service/broker
assembly. Add a fail-closed ownership allocation fence for truncated username
hashes and preserve valid legacy `UserPrefix+ordinal` users when their durable
ownership record and SID match.

## Motivation

Every pool previously derived ordinal 1 as the same local username. The second
pool therefore hit ownership isolation even though its slot and profile were
different. The mapping now uses the canonical `DataDir` installation scope,
slot ID, and ordinal, while remaining within the common Windows 20-character
local-account limit. Environment generation is deliberately excluded so a
generation update does not create a new SID.

## Test Evidence

Passed on Linux: `go test ./...`, `go vet ./...`,
`go test -race ./internal/slotwindows`, and `git diff --check`. Passed
Windows-targeted compile/vet checks: `GOOS=windows GOARCH=amd64 go vet
./internal/slotwindows` and `GOOS=windows GOARCH=amd64 go test -c
./internal/slotwindows`. Platform-neutral tests cover two pools at ordinal 1,
multiple ordinals, installation scope, long/path-shaped IDs, repeat
derivation, generation changes, and forced collision detection.

Native Windows user, ACL, WTS, and session smoke was not available on this
Linux host. The existing opt-in native smoke remains a separate deployment
gate; its legacy pre-created-user helper must be aligned by the native smoke
owner before it exercises the v2 mapping.

## Risk

The username contains an 8-hex-character truncated SHA-256 suffix to meet the
Windows length constraint. This is not used as a probabilistic uniqueness
claim: before `NetUserAdd`, the provisioner scans valid ownership records in
the installation scope and returns `ErrIdentityCollision` for a different slot
claim. Atomic ownership creation and SID checks make parallel attempts either
continue the same claim or fail closed without adopting a half-created user.

Legacy users are retained only when the slot ownership record is present and
its SID matches the existing user. Lost metadata, SID drift, foreign same-name
accounts, administrator membership, and malformed records fail closed. A
changed `DataDir` or prefix is an explicit migration boundary; this change does
not remove old profiles or sessions.

## Rollback

Stop or scale Windows pools to zero and wait for resource cleanup, then revert
the implementation commit. Do not delete local users or profiles manually; use
the provisioner's ownership-checked retirement path. Existing v2 users remain
recoverable by restoring this commit or by an explicit deployment migration.

## Breaking Change

Fresh Windows slots receive the new scoped username format. Existing correctly
owned legacy users continue to use their legacy username. Core and Matrix DTOs
remain unchanged and do not expose usernames or SIDs.

## Backport Target

None

## Handoff

- Implementation full SHAs: `ef57df4f724a00a0a357be8daa00bb4f6bed8bb1`,
  `ce94cb4ba62717bfc1c35577fcafb3501275420b`, and
  `51470a53a2430ccbe577e0906b56379314fd5584`, and
  `e95e91bbf4458326b0164dc720b2e7cc6283d005`, and
  `32727067eda9c9614bf006ad25500569a3fc2cd8`.
- Identity contract: `ManagedIdentityAlgorithm = sha256-scope-slot-v2`;
  `UserPrefix + lower_hex(SHA256(algorithm NUL scope NUL slot_id NUL ordinal))[0:8]`.
- Scope: canonical absolute `DataDir`, existing symlinks resolved, lower-cased
  and slash-normalized for the Windows case-insensitive host scope.
- Generation is not an input to username derivation; desktop/path generation
  remains slot-owned and generation-scoped.
- Legacy migration requires matching ownership `SlotID`, `Ordinal`, and `SID`;
  comment/username alone cannot authorize adoption.
- Legacy records without a username resolve the account name from the recorded
  SID and re-check the managed marker and account restrictions before reuse.
- Collision race: allocation records are scanned before OS mutation; ownership
  records are created atomically, and competing same-name claims fail closed.
- A loser of a concurrent `NetUserAdd` race marks the observed identity as
  pre-existing, so later rollback cannot delete the winner's account.
- Native status: Windows compile/vet only; no native user/ACL/WTS smoke on Linux.
- Assembly need: service and broker must call `Options.DeriveUsername` (or a
  shared contract wrapper) and must not duplicate or expose the format.
