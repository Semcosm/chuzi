# CR-0004: add the repository evolution roadmap

Base: main
Head or Range: 008b3141e7f4281aaac0eb31e516fa479ef84396
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs: add repository evolution roadmap
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 008b3141e7f4281aaac0eb31e516fa479ef84396
Head OID: 008b3141e7f4281aaac0eb31e516fa479ef84396
Integrated Result: pending

## Summary

Add a long-term repository evolution roadmap and link it from the root and
project documentation indexes. The roadmap records the completed governance
and cross-platform build baseline, the ordered implementation phases, the
dependencies between domain, storage, queue, credential, session, Matrix,
browser, and operations work, and the acceptance gates for each phase.

## Motivation

The existing documents define the architecture, state machine, security
constraints, Matrix command contract, and operational requirements, but they do
not provide one durable view of implementation order or distinguish build
coverage from real browser runtime coverage. A maintained roadmap reduces
planning drift while keeping each implementation phase independently reviewable
under UGS.

## Test Evidence

Run the repository policy, quality, supply-chain, Action pinning,
repository-shape, build-contract, adapter, and CR validators, together with
`git diff --check`. Confirm that the new document is linked from both README
indexes and that no generated or secret files are added. GitHub required checks
must pass for the pull request and the integrated main result.

## Risk

The change is documentation-only and does not activate any planned runtime
capability. The roadmap could become stale if future CRs change architecture or
phase order, so it explicitly requires status updates to land with the relevant
governed change. It avoids fixed delivery dates and does not promise native
browser support where only cross-compilation exists.

## Rollback

Revert the roadmap and index links through a subsequent UGS documentation CR.
No application state, credentials, deployment data, or published artifacts are
changed.

## Breaking Change

None. This adds planning documentation and navigation links only.

## Backport Target

None.
