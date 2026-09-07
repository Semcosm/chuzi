# UGS Bootstrap Package

The bootstrap package is generated from the UGS source tree. It is not a
second hand-maintained repository skeleton.

For local development:

```bash
scripts/ugs_init.sh --profile baseline /path/to/empty-repository
scripts/ugs_init.sh --profile standard /path/to/empty-repository
scripts/ugs_init.sh --profile high-trust /path/to/empty-repository
```

Document Map support is optional and is not installed by default. To enable
the feature, pass `--with-document-map`:

```bash
scripts/ugs_init.sh --profile standard --with-document-map /path/to/empty-repository
```

The bootstrap package then installs a minimal
`.ugs/document-map.json`, `document-map.schema.json`, and the generator and
checker scripts. The minimal template maps only `README.md`; users extend the
tree as their documentation grows. `scripts/generate_document_map.py` renders
the configured tree, while `scripts/validate_document_map.py` checks that the
README has not drifted. Standard workflows invoke the checker only when the
configuration exists.

The published package's release tests must initialize at least one consumer
with `--with-document-map` and pass both generation and validation checks. This
keeps the feature optional for consumers while making it a tested capability
of every UGS bootstrap release.

The command creates or initializes a Git repository, installs the UGS policy,
schema, CR template, policy validator, and managed hooks, then creates a
commit when Git identity is configured. `standard` additionally installs the
quality profile documents, profile validators, supply-chain evidence landing
area, and an optional GitHub compatibility adapter with a SHA-pinned workflow.
The baseline profile contains only Git-native Core material. Use `--no-commit` for a staged
initialization, `--dry-run` to inspect the plan, and `--migrate` to add only
missing UGS files to an existing repository.

`high-trust` additionally installs the public signer registry and signature
validators. It never generates or packages private keys; a normal high-trust
initialization requires the operator's SSH signing key, while `--no-commit`
supports preparing a repository before trust material is configured.

The baseline profile keeps the GitHub adapter optional. Its CR helper commands
remain as compatibility wrappers and return a clear installation message until
the repository is initialized or migrated with `--profile standard` or
`--profile high-trust`. The bare-Git update adapter and its Core ref-update
validator are included in every profile.

Every formal release builds `ugs-bootstrap-v<version>.tar.gz` from the tagged
source and publishes it, its manifest, component inventory, and SHA-256 file as
Release assets. The manifest binds the package to the source commit and
records every payload file digest. The component inventory is published as
`ugs-bootstrap-v<version>.tar.gz.components.json`, is copied into the archive
as `COMPONENTS.json`, and describes the install target, category, profile
coverage, mode, and ownership of every payload file. The single package
contains all supported profile templates; `--profile` selects the generated
repository shape. Consumers should verify
the signed release tag and checksum before extracting the package.

The archive also includes `OFFLINE-QUICKSTART.md` at its root, plus offline
copies of the applicable UGS Core, v0.3 profile, conformance, commit, review,
release, and bootstrap guidance under `docs/git/`, together with
`CONTRIBUTING.md` and `RELEASE.md`. Start with the Quick Start and then use
the local documents. When the tagged source contains a matching release
packet, the archive also includes it as `RELEASE-NOTES.md`; a downloaded
release remains usable without web access.

## Full-Component Upgrade

The package includes `scripts/ugs.sh`, `scripts/ugs_upgrade.py`, and a
machine-readable `COMPONENTS.json`. Use the package script from the extracted
release directory to upgrade an existing v0.3.x consumer:

```bash
./scripts/ugs.sh upgrade \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz \
  --dry-run /path/to/repository
./scripts/ugs.sh upgrade \
  --archive ./ugs-bootstrap-vX.Y.Z.tar.gz \
  --backup-dir /path/to/backup \
  /path/to/repository
```

The archive checksum and all four manifests are verified before writes. The
upgrade installs every component, regardless of the currently active profile,
then preserves that profile in `.ugs/policy.json`. It never implicitly
activates `standard` or `high-trust`.

The component manifest records each archive file's category, supported
profiles, destination, mode, and ownership. Categories are `core`,
`profile-specific`, `template`, `documentation`, `test`, and `release-only`.
Project-owned documents are added when missing and reported as
`project-preserved` when they differ; CR history is not copied. Use
`--overwrite-project-files` only after reviewing the dry-run output. A
filesystem conflict aborts before any write. Each successful operation creates
a recoverable backup and prints a rollback command:

```bash
./scripts/ugs.sh rollback --backup-dir /path/to/backup /path/to/repository
```

Profile activation is explicit and separate:

```bash
./scripts/ugs.sh activate --profile baseline --archive ./ugs-bootstrap-vX.Y.Z.tar.gz /path/to/repository
./scripts/ugs.sh activate --profile standard --archive ./ugs-bootstrap-vX.Y.Z.tar.gz /path/to/repository
./scripts/ugs.sh activate --profile high-trust --archive ./ugs-bootstrap-vX.Y.Z.tar.gz /path/to/repository
```

Normal, linked-worktree, and managed-worktree layouts are supported. Bare Git
repositories are detected and rejected because installation needs a worktree;
install into a checkout associated with the bare repository instead.

The release workflow includes a consumer job that downloads the published
assets through the GitHub Releases API on a clean runner. It verifies the
release tag, checksum, source commit, embedded manifest, every payload digest,
every payload category, and then initializes and upgrades clean consumer
repositories from the extracted package.
