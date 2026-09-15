# CR-0035: restore annotated tag refs for stable publishing

Base: main
Head or Range: ea06ec0e331f237429b6b69c5195c6b6c644d379
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(release): restore annotated tag before publishing
Revision: 3
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: a3b248b5ddac6b8ef31527c94dfc265c23a5c137
Head OID: a3b248b5ddac6b8ef31527c94dfc265c23a5c137
Integrated Result: pending

## Summary

Restore the formal release tag ref in the stable-release job after
`actions/checkout` and before tag validation. The job now fetches the exact
`refs/tags/<tag>` ref, requires that it resolves to an annotated tag object,
and confirms that the tag ultimately points at the workflow commit. Formal
`v<major>.<minor>.<patch>` tag pushes also trigger the build workflow directly.

## Motivation

GitHub Actions checkout materialized `v0.0.1` as a direct commit ref in the
stable-release runner even though the remote tag was a trusted annotated SSH
signature. The release validator therefore rejected a valid formal release as
a lightweight tag, leaving all built artifacts unpublished. The ref restore
keeps the release validator aligned with the repository's annotated-tag policy
and makes future formal tag pushes follow the same release path.

## Test Evidence

`./scripts/test_build_contract.sh`

`./scripts/validate_action_pinning.sh .ugs/policy.json .github/workflows`

`git diff --check`

The ref behavior was reproduced locally by overwriting a tag ref with its
commit and fetching `refs/tags/v0.0.1` back with `--force --no-tags`; the ref
resolved to the annotated tag object and passed `^{tag}` validation.

## Risk

The stable-release job now performs one additional read-only tag fetch before
validation. A missing, mismatched, lightweight, or otherwise untrusted tag
still fails closed; no release is published unless the tag object and commit
match the workflow context. Tag pushes will run the existing four-target build
matrix and stable-release job, so an incorrectly named formal tag is ignored
by the tag filter rather than published.

## Rollback

Revert the workflow trigger, tag-ref restore step, and contract assertions in a
later change. The existing manual workflow dispatch remains available for
nightly builds, and no application or release artifact state is migrated.

## Breaking Change

None. The service, launcher, browser-worker, package formats, and release
attestation contracts are unchanged.

## Backport Target

none
