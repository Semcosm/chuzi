# Contributing

This repository follows UGS Core v0.2 as declared in `REPOSITORY_POLICY.md`.

The UGS Core v0.2 normative baseline is closed. Editorial corrections still
follow the normal CR path; proposals that change normative behavior belong in the
non-normative [v0.3 roadmap](docs/roadmap/v0.3.md) first and must not be
silently mixed into a v0.2 release packet.

## Local Setup

Configure the managed hooks path after cloning:

```bash
git config core.hooksPath .githooks
git config gpg.format ssh
git config user.signingkey ~/.ssh/id_ed25519
git config commit.gpgsign true
git config tag.gpgsign true
git config gpg.ssh.allowedSignersFile keys/allowed_signers
git config gpg.ssh.revocationFile keys/revoked_signers
```

Install `jq` before running the policy-manifest validators. On Arch Linux:

```bash
sudo pacman -S --needed jq
```

The signing key you use locally must correspond to an entry in
`keys/allowed_signers`.

## Commit Signing

- All commits proposed for integration must be SSH-signed.
- The trusted signer registry is `keys/allowed_signers`; revoked or compromised
  keys are recorded in `keys/revoked_signers`.
- No bot signer is trusted by default.
- If a signing key is lost and no trusted backup remains, use the documented
  emergency path only to rotate trust material and restore signed operation.

## Branching

- Start every non-trivial change from `main`.
- Use a short-lived topic branch.
- Recommended names:
  - `docs/<scope>-<slug>`
  - `chore/<scope>-<slug>`
  - `fix/<scope>-<slug>`
  - `feat/<scope>-<slug>`
  - `refactor/<scope>-<slug>`

## Commit Messages

Commit messages must follow the UGS format:

```text
<type>(<scope>): <summary>

<body>

<footer trailers>
```

Minimum expectation for this repository:

- use one of the UGS core types
- keep the second line blank
- end the message with at least one trailer
- sign the commit with a key trusted in `keys/allowed_signers`

Example:

```text
docs(repo): clarify release verification steps

Document how to verify signed annotated release tags and where trusted
maintainer keys are published.

Refs: release-guide
Signed-off-by: Semcosm <chenzhipeng.main@gmail.com>
```

## Change Requests

The persisted `cr/CR-*.md` file is the authoritative CR for every non-trivial
change. On GitHub, the pull request is a generated review view of that CR.

The PR body must include:

- `Summary`
- `Motivation`
- `Test Evidence`
- `Risk`
- `Rollback`
- `Breaking Change`
- `Backport Target`

`base`, `head`, and `title` are provided by GitHub PR metadata and do not need
to be duplicated in the body.

Create the PR from the CR with `scripts/create_pr_from_cr.sh`. When not using
GitHub, the same persisted CR is consumed through request-pull or patch review.
Every integrated change must retain its matching `cr/CR-*.md` record.

## Review Expectations

- Maintainer-authored changes may self-integrate.
- External contributions require human review before integration.
- Sensitive paths require maintainer acknowledgment.
- Emergency changes require post-merge review.

## Local Validation

Run the repository checks before pushing:

```bash
scripts/validate_repo.sh
scripts/ugs_check.sh
scripts/validate_commit_range.sh main..HEAD
scripts/validate_commit_signatures.sh main..HEAD
scripts/validate_cr_record.sh cr/CR-0003-record-equivalent-crs.md
```

The managed hooks run the same checks automatically during commit and push.

## Protected Branches

- Do not push directly to `main` during normal operation.
- Push your topic branch and merge through a PR or equivalent CR.
- Unsigned or untrusted commits are rejected by local hooks and by the GitHub
  validation workflow.
- When integrating without the GitHub web UI, first push the topic branch and
  then fast-forward `main` with `UGS_ALLOW_MAIN_PUSH=cr`.
- The only normal bypass is the one-time bootstrap push that adopts this policy.

For high-trust integration, use GitHub PR checks for review and CI, then
fast-forward `main` locally so the integrated commits remain SSH-signed:

```bash
git fetch origin main
git switch main
git merge --ff-only origin/main
UGS_ALLOW_MAIN_PUSH=cr git push origin HEAD:main
```

Do not use a hosting-platform rebase or squash merge for high-trust changes
when the platform-generated integration commit cannot carry a trusted SSH
signature.
