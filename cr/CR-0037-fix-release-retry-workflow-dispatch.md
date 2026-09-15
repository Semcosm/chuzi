# CR-0037: fix stable release retry workflow dispatch

Base: main
Head or Range: a639e3bb5603c3d1a1deff17bdf87b18270d49b5
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(release): make stable retry workflow dispatchable
Revision: 2
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: a639e3bb5603c3d1a1deff17bdf87b18270d49b5
Head OID: a639e3bb5603c3d1a1deff17bdf87b18270d49b5
Integrated Result: pending

## Summary

Fix the stable release retry workflow's embedded Python validation commands so
the workflow YAML parses correctly on GitHub and exposes its
`workflow_dispatch` trigger. Keep the stable package and manifest validation
behavior unchanged.

## Motivation

GitHub registered the new retry workflow as a push-only workflow because the
unindented Python heredoc lines inside a YAML `run` block broke workflow event
parsing. The workflow then failed immediately on `main` and rejected manual
dispatch requests. Use inline Python commands so the workflow is valid YAML and
can publish the existing `v0.0.1` tag through its intended manual entry point.

## Test Evidence

`npx --yes prettier --check .github/workflows/release-retry.yml`

`./scripts/test_build_contract.sh`

`./scripts/validate_action_pinning.sh .ugs/policy.json .github/workflows`

`git diff --check`

## Risk

The change only rewrites shell command formatting inside the validation step.
The same index version and stable manifest checks remain enforced before any
release signing or publish operation.

## Rollback

Revert the workflow formatting commit. The previously merged retry workflow
remains available for audit, while the original tag-triggered release path is
unchanged.

## Breaking Change

None.

## Backport Target

none
