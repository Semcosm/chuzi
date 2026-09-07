# UGS Offline Quick Start

This guide is copied to the root of every UGS bootstrap release archive. It
is the shortest path from a downloaded release asset to an initialized,
locally validated repository. The steps below do not require access to the
UGS website.

## 1. Verify and unpack the release

Run these commands in the directory containing the downloaded archive and its
checksum file. Replace the tag when using another release:

```bash
tag=v0.3.27
sha256sum -c "ugs-bootstrap-${tag}.tar.gz.sha256"
tar -xzf "ugs-bootstrap-${tag}.tar.gz"
cd "ugs-bootstrap-${tag}"
```

`MANIFEST.json` records the SHA-256 digest of every file in the archive. Keep
the archive, checksum, manifest, and `COMPONENTS.json` together when
distributing the package. The archive consumer verifies the external
`.manifest.json` and `.components.json` sidecars before installation.
If the archive includes `RELEASE-NOTES.md`, read it after extraction for the
changes and verification notes specific to that version.

## 2. Initialize a repository

The target directory must be empty, or an existing repository must be passed
with `--migrate`:

```bash
# Minimal Git-native governance; no GitHub dependency.
./scripts/ugs_init.sh --profile baseline /path/to/empty-repository

# Standard profile with quality, supply-chain, and GitHub compatibility files.
./scripts/ugs_init.sh --profile standard /path/to/empty-repository

# High-trust profile with public signer metadata and signature validators.
./scripts/ugs_init.sh --profile high-trust /path/to/empty-repository
```

Use `--no-commit` to stage files without creating the initial commit, or
`--dry-run` to inspect the planned writes. High-trust initialization never
creates or packages private keys.

The optional generated README Document Map can be enabled for the standard or
high-trust profile:

```bash
./scripts/ugs_init.sh --profile standard --with-document-map \
  /path/to/empty-repository
```

For an already initialized repository, add only missing files with:

```bash
./scripts/ugs_init.sh --profile standard --migrate /path/to/repository
```

For an existing UGS v0.3.x repository, install the complete component set
without changing its active profile:

```bash
./scripts/ugs.sh upgrade \
  --archive "../ugs-bootstrap-${tag}.tar.gz" \
  --dry-run /path/to/repository
./scripts/ugs.sh upgrade \
  --archive "../ugs-bootstrap-${tag}.tar.gz" \
  --backup-dir /path/to/ugs-backup-${tag} \
  /path/to/repository
```

The dry run lists additions, updates, project-owned files that will be
preserved, and filesystem conflicts. Existing README, policy, workflow, trust
files, and CR history are preserved by default. A conflict stops the upgrade
before any write; `--overwrite-project-files` is an explicit opt-in for
replacing project-owned files. The command prints the exact rollback command.

The active profile remains unchanged even though all profile components are
installed. Activate a profile only as a separate step:

```bash
./scripts/ugs.sh activate --profile standard \
  --archive "../ugs-bootstrap-${tag}.tar.gz" /path/to/repository
```

Use `baseline`, `standard`, or `high-trust`. To roll back, use the package's
script and the backup directory printed by the upgrade:

```bash
./scripts/ugs.sh rollback \
  --backup-dir /path/to/ugs-backup-${tag} /path/to/repository
```

The installer supports normal `.git` directories, linked-worktree `.git`
files, and managed worktrees with `.git-worktree`. It detects bare Git
repositories and explains that a worktree checkout is required.

## 3. Run local checks

All commands below use files shipped in this archive. The baseline profile
supports the Core policy and bare-Git update path:

```bash
cd /path/to/repository
scripts/validate_policy_manifest.sh .ugs/policy.json
git config core.hooksPath .githooks
```

For the standard profile, also run the installed profile checks:

```bash
cd /path/to/repository
scripts/validate_policy_manifest.sh .ugs/policy.json
scripts/validate_quality_profile.sh .ugs/policy.json
scripts/validate_supply_chain_profile.sh .ugs/policy.json
scripts/validate_repository_shape.sh .ugs/policy.json
```

When a Document Map is enabled, verify it with:

```bash
cd /path/to/repository
scripts/generate_document_map.py --check
scripts/validate_document_map.py
```

The baseline GitHub CR helper commands are intentional compatibility wrappers.
They explain how to install the optional GitHub adapter; use the standard or
high-trust profile when GitHub PR integration is required.

## 4. Read the local reference

The release archive includes the applicable UGS guidance under `docs/git/`:

- `docs/git/ugs-bootstrap.md` — package behavior and profile selection
- `docs/git/ugs-core.md` — Git-native governance primitives
- `docs/git/ugs-v0.3-profile.md` — adopted policy and conformance profile
- `docs/git/ugs-conformance-levels.md` — profile matrix and levels
- `docs/git/ugs-branch-profiles.md` — branch behavior
- `docs/git/commit-convention.md` — commit message format
- `docs/git/review-policy.md` — review and evidence rules
- `docs/git/release-policy.md` — signed release rules
- `docs/git/ugs-quality-profile.md` — quality checks
- `docs/git/ugs-supply-chain-profile.md` — supply-chain checks
- `docs/git/ugs-repository-shapes.md` — repository capability shapes
- `docs/git/ugs-document-map.md` — optional documentation map
- `docs/git/ugs-conformance-fixtures.md` — conformance fixture overview

`CONTRIBUTING.md` and `RELEASE.md` are included as local reference copies.
Commands in those two files that refer to files outside this bootstrap bundle
are maintainer workflows for a full UGS source checkout; the consumer steps in
this guide are self-contained in the downloaded archive.

The optional GitHub adapter can contact GitHub and therefore may require
network access and credentials. Initialization, Core validation, profile
validation, and the documentation above remain available offline.
