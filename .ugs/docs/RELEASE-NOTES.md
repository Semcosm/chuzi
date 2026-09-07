# UGS v0.3.27

This superseding patch release adds a complete, offline component upgrade flow
for existing UGS repositories.

## Summary

The release package now includes `scripts/ugs.sh`, `scripts/ugs_upgrade.py`,
and a signed-release component inventory in `COMPONENTS.json` plus its
`.components.json` sidecar. Existing UGS repositories can use `upgrade` to
install the complete component set while preserving the currently active
profile. Profile changes remain explicit through `activate`.

The upgrade path verifies the archive checksum, embedded and external package
manifests, component classification, file modes, archive paths, and supported
Git worktree layout before writing. It provides dry-run output, conflict
detection, project-owned file preservation, recoverable backups, and rollback.
The release package's offline documentation explains the full workflow without
requiring a web tutorial.

## Compatibility

This release does not modify or replace `v0.3.26` or any earlier immutable
release. Existing initialization commands and profiles remain available.
Repositories must already be initialized with UGS before using `upgrade`;
fresh repositories continue to use `ugs_init.sh`. Bare Git object stores are
rejected for upgrades because installation requires a worktree checkout.

`v0.3.26` and earlier release downloads remain supported without a component
sidecar. Releases beginning with `v0.3.27` require the component inventory for
the full upgrade protocol.

## Verification

Before tagging, run:

```bash
scripts/validate_repo.sh
scripts/ugs_check.sh --format json
scripts/test_bootstrap_upgrade.sh
scripts/test_bootstrap_package.sh
scripts/test_bootstrap_equivalence.sh
scripts/test_profile_conformance.sh
scripts/validate_cr_coverage.sh HEAD
```

After publication, verify the signed tag and downloaded package:

```bash
scripts/validate_release_tag.sh v0.3.27
scripts/test_bootstrap_release.sh v0.3.27
```

For offline use, extract the archive and begin with `OFFLINE-QUICKSTART.md`,
`README.md`, and `RELEASE-NOTES.md`. Keep the archive, checksum, manifest, and
component sidecar together.

## Rollback

Do not delete, replace, or force-update `v0.3.26` or `v0.3.27`. If a published
asset is defective, preserve the immutable release and publish a later
superseding patch. For a consumer upgrade, use the backup directory printed by
the command:

```bash
scripts/ugs.sh rollback --backup-dir /path/to/backup /path/to/repository
```

## Breaking Change

No. The new component inventory is required for the new upgrade protocol, but
legacy releases retain their existing initialization compatibility.

## Backport Target

None.
