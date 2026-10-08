# UGS v0.3.33

This patch release hardens CR provenance for hosted `rebase-ff` integrations.

## Summary

The release preserves the original reviewed `Head OID` when a hosting platform
rewrites the source series, proves equivalence through a canonical non-CR tree
diff, and provides a bounded lifecycle for closing the pending CR after the
rewritten result is known.

## Included scope

- accept a patch-equivalent hosted rebase result under the `rebase-ff` strategy;
- require source/result tree-diff equivalence while excluding persisted CR metadata;
- validate metadata-only closure commits against the prior pending or accepted CR;
- reject unrelated CRs, stale bases, changed patches, and code changes in closure commits;
- add disposable range fixtures and document the portable Core behavior; and
- publish the v0.3.33 bootstrap package and signed release packet.

## Compatibility

The policy version remains `0.3`, and literal fast-forward, merge, and squash
rules remain unchanged. Hosted rebase integrations gain an explicit
patch-equivalence path; signature, review-trailer, and protected-ref checks
remain required. The post-tag supply-chain evidence for v0.3.33 is a separate
signed record that binds the immutable tag and published archive digest.

## Verification

The release candidate must pass:

    scripts/validate_repo.sh
    scripts/ugs_check.sh --format json
    scripts/test_conformance.sh
    scripts/test_profile_conformance.sh
    scripts/test_main_cr_range.sh
    scripts/test_bootstrap_package.sh
    scripts/test_bootstrap_equivalence.sh
    scripts/test_git_fixtures.sh
    scripts/validate_document_map.py
    scripts/validate_cr_coverage.sh HEAD
    scripts/validate_commit_range.sh v0.3.32..HEAD
    scripts/validate_commit_signatures.sh v0.3.32..HEAD

After the signed tag exists, build the package twice with
`SOURCE_DATE_EPOCH=0`, publish the archive, and run
`scripts/test_bootstrap_release.sh v0.3.33`. The post-tag evidence CR must
also pass `scripts/validate_supply_chain_release.sh v0.3.33 .ugs/policy.json
Semcosm/UGS` after its evidence is integrated.

## Rollback

Release tags are append-only. Do not delete, replace, or force-update
`v0.3.32` or `v0.3.33`. If the validator, archive, or evidence is defective,
retain the immutable objects and publish a later signed superseding patch
through a new CR.

## Breaking Change

No. The change adds a verifiable hosted-rebase provenance form and closure
checks without changing the v0.3 policy wire values.

## Backport Target

None.
