# CR-0001: Align the consumer repository with UGS standard governance

Base: main
Head or Range: 2c0ac9bcb9023b6d71643714c19d6cb11fa11ac3
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: chore(governance): align repository with UGS standard profile
Revision: 2
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: b2fc1e418b01f9cd69e91d024a1ba6f3374b97d5
Head OID: 2c0ac9bcb9023b6d71643714c19d6cb11fa11ac3
Integrated Result: pending

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
