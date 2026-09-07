# Release Guide

This repository uses signed annotated tags as formal release objects.

Release notes are kept under `releases/` using the filename
`v<major>.<minor>.<patch>.md`. A release packet records the exact scope and
verification evidence; it is not a published release until the corresponding
trusted signed tag exists. The current v0.2 closure packet is
[`releases/v0.2.0.md`](releases/v0.2.0.md).

## Release Object

- Tag form: `v<major>.<minor>.<patch>`
- Tag type: signed annotated tag
- Versioning: `semver`

## Release Preconditions

Before creating a release tag, ensure:

- the target commit is explicit
- release notes are prepared
- compatibility impact is understood
- rollback guidance is available when applicable
- the working tree and index are clean
- repository, commit-message, CR, and signature validation has passed

## Signing

- Release signers are repository maintainers whose SSH signing keys are trusted
  in `keys/allowed_signers`.
- Revoked or compromised signing keys are recorded in `keys/revoked_signers`.
- Key rotation and revocation events must be documented in release notes or a
  repository notice before the next formal release.
- The same trust registry is used to verify protected-branch commits and formal
  release tags.

## Create A Release

```bash
git checkout main
git pull --ff-only
git diff --quiet
git diff --cached --quiet
scripts/validate_repo.sh
scripts/validate_commit_range.sh <previous-release>..HEAD
scripts/validate_commit_signatures.sh <previous-release>..HEAD
git tag -s vX.Y.Z -m "UGS vX.Y.Z"
git push origin vX.Y.Z
```

## High-Trust Integration

GitHub PR checks provide review and CI evidence, but a hosting-platform rebase
or squash operation may create an unsigned integration commit. For this
repository, integrate an approved topic branch with the local signed
fast-forward path before creating a formal release tag:

```bash
git fetch origin main
git switch main
git merge --ff-only origin/main
UGS_ALLOW_MAIN_PUSH=cr git push origin HEAD:main
```

The topic branch must already be pushed and have a matching CR record. The
pre-push hook re-runs repository, CR, commit-message, and signature checks.

Replace `<previous-release>` with the preceding release tag. For the initial
v0.2.0 tag, validate from the high-trust signing anchor instead. For the v0.3
release candidate, validate from the recovered signed fast-forward boundary:

```bash
scripts/validate_commit_range.sh 5cc6c9344b657354f463cf06fbb7d38f964a9c6d^..HEAD
scripts/validate_commit_signatures.sh 5cc6c9344b657354f463cf06fbb7d38f964a9c6d^..HEAD
scripts/validate_commit_range.sh 459572bf2910267659b3bb590fdf1b00b26ed94f^..HEAD
scripts/validate_commit_signatures.sh 459572bf2910267659b3bb590fdf1b00b26ed94f^..HEAD
```

Do not use an unsigned or lightweight tag as a substitute for the formal
release object.

## Verify A Release

```bash
git fetch --tags origin
git -c gpg.format=ssh \
  -c gpg.ssh.allowedSignersFile=keys/allowed_signers \
  -c gpg.ssh.revocationFile=keys/revoked_signers \
  tag -v vX.Y.Z
```

If verification fails:

- do not consume the release
- confirm the signer is present in `keys/allowed_signers`
- check `keys/revoked_signers` and any published key-rotation notice
- report the failure before proceeding

Formal release tags are append-only. Never delete, force-update, or replace an
existing `v*` tag; publish a superseding patch release when correction is
needed.
