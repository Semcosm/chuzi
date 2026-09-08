# CR-0007: document the GitHub CLI repository workflow

Base: main
Head or Range: d86647ee4f4ae7f6bc2827174b269a93d5cf04f0..262a1a9686eb4b6762bc88f3374b4b76545e9579
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs: document GitHub CLI repository workflow
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: d86647ee4f4ae7f6bc2827174b269a93d5cf04f0
Head OID: 262a1a9686eb4b6762bc88f3374b4b76545e9579
Integrated Result: pending

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
credential material in `AGENTS.md` or this CR. The already-integrated main
checks remain green: `ugs-validate` run `34249990381` and aggregate
`chuzi-build` run `34249990344`.

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
