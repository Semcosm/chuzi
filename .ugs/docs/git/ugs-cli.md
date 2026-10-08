# UGS CLI Usage

This guide is the task-oriented command reference for UGS v0.3 repositories.
Use it together with [UGS Core](ugs-core.md) for governance semantics and
[UGS Bootstrap Package](ugs-bootstrap.md) for package and component rules.

## Choose the command path

| Situation | Command | Result |
| --- | --- | --- |
| Empty directory | `scripts/ugs_init.sh --profile ...` | Creates the initial UGS governance files |
| Existing UGS repository | `scripts/ugs.sh migrate ...` | Installs the complete release component set |
| Compatibility invocation | `scripts/ugs.sh upgrade ...` | Same migration engine; `migrate` is preferred |
| Change active profile | `scripts/ugs.sh activate ...` | Changes the declared profile explicitly |
| Close an integrated topic branch | `scripts/ugs.sh branch close ...` | Retires the local branch after safety checks |
| Restore a migration | `scripts/ugs.sh rollback ...` | Restores a migration backup and hooks setting |
| Validate a complete UGS checkout | `scripts/ugs_check.sh --format json` | Runs the repository conformance suite |

`ugs_init.sh` is the bootstrap entry point. A direct initialization creates the
policy, schemas, CR templates, hooks, and profile-specific validators. The
full `scripts/ugs.sh` command surface and offline documentation are installed
when an existing UGS repository consumes a release package with `migrate` or
`upgrade`. This distinction lets a minimal baseline repository start without
copying the entire release toolchain.

## Verify and unpack a release package

Keep the archive, checksum, manifest, and component inventory together:

`@bash
tag=vX.Y.Z
sha256sum -c "ugs-bootstrap-$tag.tar.gz.sha256"
tar -xzf "ugs-bootstrap-$tag.tar.gz"
cd "ugs-bootstrap-$tag"
`

The package scripts verify the archive checksum, embedded `MANIFEST.json`,
external manifest, and `COMPONENTS.json` before a migration writes anything.
Use the signed release tag and the published checksum as the trust boundary.

## Initialize a repository

The target must be empty unless `--migrate` is explicitly supplied:

`@bash
scripts/ugs_init.sh --profile baseline /path/to/empty-repository
scripts/ugs_init.sh --profile standard /path/to/empty-repository
scripts/ugs_init.sh --profile high-trust /path/to/empty-repository
`

Use `--dry-run` to inspect planned writes and `--no-commit` to stage the files
without creating the bootstrap commit. `--with-document-map` is available for
standard and high-trust repositories that want generated README navigation.
High-trust initialization installs public signer metadata only; it never
creates or packages private keys.

After initialization, run the checks provided by the selected profile:

`@bash
cd /path/to/repository
scripts/validate_policy_manifest.sh .ugs/policy.json
`

Standard and high-trust repositories can additionally run the installed
quality, supply-chain, action-pinning, repository-shape, and signer validators.

## Migrate or upgrade an existing repository

Use `migrate` for the explicit offline migration path. `upgrade` remains a
compatibility alias for the same engine.

Preview the change first:

`@bash
./scripts/ugs.sh migrate \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz \
  --dry-run \
  --report /path/to/migration-dry-run.json \
  /path/to/repository
`

Apply it with a backup outside the target repository:

`@bash
./scripts/ugs.sh migrate \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz \
  --backup-dir /path/to/ugs-backup-vX.Y.Z \
  --report /path/to/migration-report.json \
  /path/to/repository
`

The migration installs all package components while preserving the repository's
active profile. It adds missing project-owned files and reports differing
project-owned files as `project-preserved`. CR history is never copied. A
filesystem conflict stops the migration before any write. Use
`--overwrite-project-files` only after reviewing the dry-run report.

The `ugs-migration/v1` report records the package identity, operation counts,
file digests, backup path, and rollback command. The compatibility form is:

`@bash
./scripts/ugs.sh upgrade \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz \
  --backup-dir /path/to/ugs-backup-vX.Y.Z \
  /path/to/repository
`

## Activate a profile explicitly

Installing all components does not activate a stronger profile. Activate it as
a separate, reviewable operation:

`@bash
./scripts/ugs.sh activate --profile baseline \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz /path/to/repository
./scripts/ugs.sh activate --profile standard \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz /path/to/repository
./scripts/ugs.sh activate --profile high-trust \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz /path/to/repository
`

`baseline`, `standard`, and `high-trust` are the supported profile values.
Activation changes the policy declaration and installation metadata; it does
not copy or delete profile components.

## Close or archive a topic branch

After an integrated change has a matching CR record, close its declared topic
branch:

`@bash
./scripts/ugs.sh branch close feat/example --target main --remote origin
./scripts/ugs.sh branch close feat/example --dry-run --format json
./scripts/ugs.sh branch close feat/abandoned --archive --reason "superseded"
`

The normal path checks protected refs, declared topic prefixes, target reachability,
integrated CR evidence, worktree use, and remote OID leases before deleting a
branch. `--dry-run` changes no refs. Archive mode preserves the source tip under
`refs/ugs/archive/<branch>` and requires an explicit reason. JSON output uses
`ugs-branch-close/v1` and repeated invocations are idempotent.

## Roll back a migration

Use the backup path printed by `migrate`, `upgrade`, or `activate`:

`@bash
./scripts/ugs.sh rollback \
  --backup-dir /path/to/ugs-backup-vX.Y.Z \
  --report /path/to/rollback-report.json \
  /path/to/repository
`

Rollback verifies restored file digests, permissions, and the previous
`core.hooksPath` value. Do not delete or replace signed release tags when a
consumer migration needs correction; repair the repository through a later
reviewed change or restore the external backup.

## Validate and automate

For the UGS source repository, run the full conformance report:

`@bash
scripts/ugs_check.sh --format json
scripts/validate_repo.sh
`

Consumer repositories use the validators installed by their active profile.
The full `ugs_check.sh` and `validate_repo.sh` suites are source-repository
validation tools and are not part of the minimal direct `ugs_init.sh` output.

Agents and CI should use this sequence:

1. Verify the signed release tag and checksum.
2. Run `migrate --dry-run` and save the JSON report.
3. Review conflicts and project-preserved files.
4. Run `migrate` with an external backup and report path.
5. Run the profile validators and retain the report with the change evidence.
6. Use `rollback` only with the recorded backup when recovery is required.

All write-capable commands should be invoked from the extracted release
directory so the package scripts and sidecar manifests are from the same
release.

## Command help

The wrapper provides a stable top-level help surface:

`@bash
scripts/ugs.sh --help
scripts/ugs.sh help
scripts/ugs.sh init --help
scripts/ugs.sh migrate --help
scripts/ugs.sh activate --help
scripts/ugs.sh rollback --help
scripts/ugs.sh branch close --help
`

Use the command-specific help for option details; use this guide for the
workflow and safety rules behind each command.
