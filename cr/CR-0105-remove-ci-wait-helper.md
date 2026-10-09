# CR-0105: use GitHub CLI run watch directly

Base: main
Head or Range: 4c0f748fc5811663b633141ad9852fa5681e595d
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: docs(ci): use gh run watch directly
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 7345dfa439d4dc2dc93909db9dbabcff31c50ac7
Head OID: 4c0f748fc5811663b633141ad9852fa5681e595d
Integrated Result: pending

## Summary

Remove the repository-specific `scripts/ci-wait` helper and document the
standard GitHub CLI `gh run watch` command for bounded workflow observation.

## Motivation

The helper duplicated GitHub CLI behavior and added a second run-discovery and
timeout interface. `gh run watch <run-id> --interval 30 --exit-status` already
provides the required direct run association and failure status.

## Test Evidence

~~~text
git diff --check
./scripts/validate_cr_record.sh cr/CR-0105-remove-ci-wait-helper.md
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh
~~~

## Risk

Only agent-facing CI guidance and the obsolete helper are changed. Workflow
definitions and application behavior are unaffected.

## Rollback

Restore `scripts/ci-wait` and the previous GitHub Actions guidance in
`AGENTS.md`.

## Breaking Change

None.

## Backport Target

None.
