# CR-0001: Align the consumer repository with UGS standard governance

Base: main
Head or Range: 30b2f127a0ccacd652fce0e475e0ebcddf1ad297
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: chore(governance): align repository with UGS standard profile
Revision: 3
Status: integrated
Decision: accepted
Policy Version: v0.3
Base OID: 30b2f127a0ccacd652fce0e475e0ebcddf1ad297
Head OID: 30b2f127a0ccacd652fce0e475e0ebcddf1ad297
Integrated Result: main@30b2f127a0ccacd652fce0e475e0ebcddf1ad297

## Summary

Activate the official UGS v0.3.27 `standard` profile, synchronize the
consumer-owned GitHub governance files, replace the source-repository
validation workflow with the standard consumer workflow, and establish a
repository-dedicated SSH signing identity.

## Motivation

The repository was declaring the `baseline` profile while carrying a GitHub
Actions workflow copied from the UGS source repository. That workflow invoked
test and validator scripts which are not part of the consumer package, so the
latest Actions run failed before policy validation. Standard governance must
use the profile's packaged components and its consumer-specific workflow. The
previous signing key was shared with another repository, so this revision
rotates the active signer to a key generated specifically for chuzi and
revokes the previous trust entries.

The original governance change used base OID
`b2fc1e418b01f9cd69e91d024a1ba6f3374b97d5` and signed source commits
`2c0ac9bcb9023b6d71643714c19d6cb11fa11ac3` and
`418dbdaf1e405bca86e9ca1d1508f1bb66c934b7`. GitHub's rebase integration
generated `5415bd45ae2d4814385f1dfa1d68feb35518f4c1` and
`30b2f127a0ccacd652fce0e475e0ebcddf1ad297`, which changed the commit object
IDs and removed the source SSH signatures from the generated objects. This
revision records the generated mainline result and closes the CR against the
current main revision without rewriting any existing history.

## Test Evidence

The official `ugs-bootstrap-v0.3.27.tar.gz` checksum and component manifests
were verified. The following checks pass in the working tree:

- `scripts/validate_policy_manifest.sh`
- `scripts/validate_quality_profile.sh`
- `scripts/validate_supply_chain_profile.sh`
- `scripts/validate_action_pinning.sh`
- `scripts/validate_repository_shape.sh`
- `scripts/validate_signer_roles.sh`
- `scripts/validate_commit_signatures.sh`
- `git diff --check`

The original pull-request validation passed in run `34184200798`. The first
post-merge main validation was run `34184304138` and failed only because the
record still named the pre-rebase head `2c0ac9bcb9023b6d71643714c19d6cb11fa11ac3`.

## Risk

The active profile now requires signed commits by policy declaration and
changes the CI surface to standard consumer checks. The repository-specific
signing key must also be registered as a GitHub signing key before GitHub can
display the new commits as verified. Release signing, protected-branch
enforcement, and maintainer review must remain configured at the hosting
boundary.

## Rollback

Use the recorded UGS backup directories to restore the prior component and
profile metadata when available. Otherwise revert the integrated change
through a subsequent CR and explicitly reactivate `baseline` with the same
verified UGS release archive. Do not rewrite existing release tags.

## Breaking Change

No runtime service behavior changes. Repository contributors must follow the
standard profile's signed-commit and review-evidence requirements.

## Backport Target

None.
