# CR-0012: record the integrated Matrix boundary in the roadmap

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs(roadmap): mark Matrix boundary complete
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: ff6b02901e05b07c7b9133e5c3774613bda25565
Head OID: ff6b02901e05b07c7b9133e5c3774613bda25565
Integrated Result: pending

## Summary

Update `docs/roadmap.md` to record the stage-six Matrix boundary as complete
after CR-0011 was integrated. Keep the production Matrix client explicitly
deferred to the later deployment stage.

## Motivation

The transport-neutral Matrix adapter, durable notification outbox, and their
tests are already present on main at the CR-0011 integration result. Leaving
the roadmap in an in-progress state contradicts the implementation and its
accepted change record. This change synchronizes documentation only; it does
not add runtime behavior or claim a production Matrix connection.

## Test Evidence

The updated roadmap cites CR-0011 and preserves its transport-neutral scope.
Run `./scripts/validate_cr_record.sh cr/CR-0012-roadmap-matrix-boundary.md`,
the repository policy, quality, supply-chain, action-pinning, shape, and build
contract validators, plus `git diff --check`.

## Risk

The risk is limited to documentation drift. The wording distinguishes the
completed transport-neutral boundary from the deferred production Matrix
client, so it does not change operational capabilities or security posture.

## Rollback

Revert the roadmap synchronization commit through a subsequent CR. The
already-integrated CR-0011 implementation and its persisted record remain
unchanged.

## Breaking Change

None. This is a documentation-only synchronization.

## Backport Target

none
