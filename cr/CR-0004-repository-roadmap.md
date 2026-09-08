# CR-0004: add the repository evolution roadmap

Base: main
Head or Range: 008b3141e7f4281aaac0eb31e516fa479ef84396..4461355f9cc22a53239872a7cc63d07e90fbbd05
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs: add repository evolution roadmap
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: d7e09a000cd4bdade7440f608b530b83abcb476c
Head OID: d7e09a000cd4bdade7440f608b530b83abcb476c
Integrated Result: main@d7e09a000cd4bdade7440f608b530b83abcb476c

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

Local policy, quality, supply-chain, Action pinning, repository-shape,
build-contract, adapter, CR, and `git diff --check` validations passed. PR #7
passed `ugs-validate` run `34206755312` and `chuzi-build` run `34206755440`,
including all four target jobs. The integrated main `chuzi-build` run
`34206914708` passed. The first main `ugs-validate` run `34206914616` correctly
rejected the still-pending record because GitHub's rebase produced
`main@d7e09a0`; this closure revision binds the record to that integrated
result. The closure PR and its resulting main checks are required to complete
the governance record.

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
