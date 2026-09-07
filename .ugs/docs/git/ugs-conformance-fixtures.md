# UGS Portable Conformance Fixtures

UGS publishes a versioned fixture corpus at
[`tests/conformance/manifest.json`](../../tests/conformance/manifest.json).
Each entry names its input, expected result, and, for negative cases, the
portable failure reason. It covers policy manifests, CR structure, review
trailers, SBOMs, build records, and release attestations.

`scripts/conformance.py` is an independent implementation using only the
Python standard library. `scripts/test_conformance.sh` runs it and the Bash
validators against the same cases and fails if pass/fail decisions diverge.
It also materializes the CR template in a new Git repository, so OID,
ancestry, and integrated-result checks do not use UGS history.

`scripts/test_git_fixtures.sh` creates and removes a disposable repository,
generates an SSH key, verifies signed commits and annotated tags, configures
hooks, exercises rebase/merge/squash, and checks protected-ref updates.

Other implementations can consume the JSON catalog and compare normalized
`pass`/`fail` results and failure reasons. Git OIDs are generated locally for
each disposable run and are intentionally not tied to this repository.

The end-to-end profile gate is `scripts/test_profile_conformance.sh`. Its
matrix is recorded in `tests/conformance/profile-matrix.json` and covers
generated consumer repositories, hooks, CRs, all declared integration modes,
migration/dry-run/idempotence, protected refs, quality and supply-chain
requirements, and temporary-key high-trust signing checks. It prints one
result per profile and fails if any profile requirement fails.

The bootstrap package tests include `scripts/test_bootstrap_upgrade.sh`. This
fixture builds a deterministic release, verifies the embedded and external
component inventories, rejects tampered manifests and archives before writing,
and exercises full-component upgrades, explicit profile activation, rollback,
project-owned file preservation, filesystem conflicts, normal and linked
worktrees, managed worktrees, and bare-repository rejection. The upgrade path
is therefore tested as a consumer-facing operation rather than only as a
package construction detail.

`scripts/validate_cr_coverage.sh` audits the first-parent history after the
adopted coverage anchor and fails on an OID not named by a persisted CR. A
commit that adds or modifies its persisted CR is also covered. The historical
coverage migration is recorded in `cr/CR-0047-historical-main-coverage.md`.
