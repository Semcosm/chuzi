# CR-0007: document the GitHub CLI repository workflow

Base: main
Head or Range: d86647ee4f4ae7f6bc2827174b269a93d5cf04f0..6eebd00877c2f73193804d193fdd2173253ee1d0
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs: document GitHub CLI repository workflow
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 6eebd00877c2f73193804d193fdd2173253ee1d0
Head OID: 6eebd00877c2f73193804d193fdd2173253ee1d0
Integrated Result: main@6eebd00877c2f73193804d193fdd2173253ee1d0

## Summary

Document the repository-specific GitHub CLI workflow in `AGENTS.md`, including
credential handling, read-only PR and Actions inspection, CR-backed PR body
synchronization, SSH topic-branch pushes, and UGS rebase-fast-forward merges.
Keep all PATs, private keys, and other credentials outside the repository.

## Motivation

The repository is operated through GitHub Actions and UGS change records, but
the exact `gh` commands and authentication boundaries were previously implicit.
Making the workflow explicit reduces accidental credential exposure, prevents
PR body/CR drift, and preserves the required provenance when GitHub rewrites a
topic branch during rebase integration.

## Test Evidence

The policy, quality, supply-chain, Action pinning, repository-shape,
build-contract, GitHub adapter, signer-role, CR, and `git diff --check`
validators pass. The sensitive credential scan found no PAT, private key, or
credential material in `AGENTS.md` or this CR. PR #13 passed
`ugs-validate` run `34251991461` and aggregate `chuzi-build` run
`34251991010`. GitHub's rebase integration produced
`main@6eebd00877c2f73193804d193fdd2173253ee1d0`; the first post-merge
`ugs-validate` run `34252349831` correctly rejected the pending record because
it still named the pre-rebase topic SHA, while main `chuzi-build` run
`34252350444` passed. This closure revision binds the record to the actual
integrated main result.

## Risk

This changes repository guidance only. The documented local credential path is
not a repository secret and no credential value is recorded. Incorrect command
examples could cause PR or branch operations to target the wrong repository, so
all examples use the explicit `Semcosm/chuzi` repository selector where
applicable.

## Rollback

Revert this documentation change through a subsequent UGS CR. No application
state, GitHub credential, workflow, or runtime data is changed by the revert.

## Breaking Change

None. The change makes the existing GitHub and UGS operating procedure
explicit without changing repository policy or automation behavior.

## Backport Target

None.
