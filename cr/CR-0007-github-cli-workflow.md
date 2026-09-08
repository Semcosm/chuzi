# CR-0007: document the GitHub CLI repository workflow

Base: main
Head or Range: d86647ee4f4ae7f6bc2827174b269a93d5cf04f0
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs: document GitHub CLI repository workflow
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: d86647ee4f4ae7f6bc2827174b269a93d5cf04f0
Head OID: d86647ee4f4ae7f6bc2827174b269a93d5cf04f0
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

Required before integration: run the policy, quality, supply-chain, Action
pinning, repository-shape, build-contract, GitHub adapter, signer-role, CR,
and `git diff --check` validators. Verify that the documented commands do not
contain a token or private key and that the GitHub workflow continues to pass
`ugs-validate` and the aggregate `chuzi-build` check.

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
