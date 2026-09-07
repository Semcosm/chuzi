# UGS Bootstrap Package

This directory is the source for the versioned UGS bootstrap package. It is
not a hand-maintained copy of an initialized repository. The release builder
packages it together with the generator and the current policy schema.

Start with [OFFLINE-QUICKSTART.md](OFFLINE-QUICKSTART.md) for a complete,
offline consumer walkthrough. Run `scripts/ugs_init.sh --help` for the local
development entry point.
The generated package supports `baseline`, `standard`, and `high-trust`
profiles. High-trust output contains public trust metadata only; private keys
remain with the operator.

## Optional Document Map

Document Map governance is opt-in. Enable it during initialization with:

```bash
scripts/ugs_init.sh --profile standard --with-document-map /path/to/empty-repository
```

This installs `.ugs/document-map.json`, its schema, and the generator/checker
scripts. The starter configuration maps only the repository README; add
document nodes to the configuration and run
`scripts/generate_document_map.py` to render the README section. Run
`scripts/validate_document_map.py` before committing. Standard workflows run
the checker automatically whenever `.ugs/document-map.json` exists.

Repositories that do not opt in receive no Document Map files and are not
required to use this feature. Release tests always exercise the opt-in path so
the published package cannot silently ship a broken Document Map capability.

The baseline profile keeps the GitHub adapter optional. Its CR helper wrappers
report how to enable the adapter instead of failing on a missing target. Use
`--profile standard` or `--profile high-trust` (with `--migrate` for an existing
baseline repository) to install the GitHub adapter;
the bare-Git update adapter and its Core ref-update validator are included in
every profile.

## Upgrade An Existing Repository

The release package also contains `scripts/ugs.sh`, `scripts/ugs_upgrade.py`,
and `COMPONENTS.json`. These provide a full-component upgrade path for an
existing UGS repository. The installer verifies the archive checksum, the
embedded `MANIFEST.json`, the external manifest, and the component manifest
before it writes anything.

From the extracted release directory, first inspect the plan:

```bash
./scripts/ugs.sh upgrade \
  --archive ./ugs-bootstrap-v0.3.27.tar.gz \
  --dry-run /path/to/existing-repository
```

Then install with a backup outside the target repository:

```bash
./scripts/ugs.sh upgrade \
  --archive ./ugs-bootstrap-v0.3.27.tar.gz \
  --backup-dir /path/to/ugs-backup-v0.3.27 \
  /path/to/existing-repository
```

`upgrade` installs the complete component set, including standard and
high-trust files, but preserves the current `.ugs/policy.json` profile. It
does not activate a stronger profile. Existing project-owned documents are
reported as `project-preserved`; CR history is never part of the component
set. Use `--overwrite-project-files` only after reviewing the dry-run report.
Filesystem conflicts abort before any write. The command prints a rollback
command that restores the backup and the previous `core.hooksPath` setting.

After the full component set is installed, activate a profile explicitly:

```bash
./scripts/ugs.sh activate --profile standard \
  --archive ./ugs-bootstrap-v0.3.27.tar.gz \
  /path/to/existing-repository
```

Use `--profile baseline`, `standard`, or `high-trust`. A profile activation
only changes the policy declaration and activation metadata; it does not copy
or delete profile components. A bare Git repository is detected and rejected
because it has no worktree. Normal, linked-worktree, and managed-worktree
layouts are supported, including a managed `.git-worktree` directory with a
placeholder `.git` file.

`COMPONENTS.json` classifies every archive file as `core`, `profile-specific`,
`template`, `documentation`, `test`, or `release-only`, and records whether
the destination is UGS-owned or project-owned. Keep the archive, `.sha256`,
`.manifest.json`, and `.components.json` together for offline installation.

## Offline UGS Documentation

The release archive includes the UGS guidance needed to use the package
without opening the project website. Start with
[`OFFLINE-QUICKSTART.md`](OFFLINE-QUICKSTART.md) and
`docs/git/ugs-bootstrap.md`, then use the local Core, v0.3 profile,
conformance-level, commit, review, and release policy documents under
`docs/git/`. `CONTRIBUTING.md` and `RELEASE.md` are included as local
operational guides. These documents are copied from the same tagged source as
the package and are listed with checksums in `MANIFEST.json`. When available,
`RELEASE-NOTES.md` is the release packet for the archive's version.
