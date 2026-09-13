# CR-0024: establish a verifiable stable release chain

Base: main
Head or Range: b09a4c03036c5e5685033811c656dca12dae3af9..pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(release): add signed stable release verification chain
Revision: 3
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: 2edabbe589c3205d51568b8ca3ae01813a7c1c1b
Head OID: af1c82d1092bc8c05eac32d8f08164623e057a46
Integrated Result: pending

## Summary

Add a stable-release path on top of the existing four-target nightly build.
Signed annotated semver tags trigger the build matrix; every target archive is
bound to the tag and commit by a UGS release attestation containing its SHA-256
digest and builder identity. The release job verifies trusted SSH signatures
and artifact digests before publishing a GitHub Release. Local scripts provide
the same generation, signing, and verification behavior without requiring a
network or real account.

## Motivation

Nightly artifacts currently have integrity sidecars but are intentionally
ephemeral and unsigned. Stable release policy already requires annotated,
trusted tags, yet no workflow connected that policy to immutable artifact
evidence. This change closes that supply-chain gap while keeping signing keys
outside the repository and without adding browser downloads, credentials, or
production service access.

## Test Evidence

Local checks:

- `scripts/test_release_signing.sh`
- `scripts/test_build_contract.sh`
- `scripts/validate_action_pinning.sh`
- `scripts/validate_policy_manifest.sh`
- `scripts/validate_quality_profile.sh`
- `scripts/validate_supply_chain_profile.sh`
- `scripts/validate_repository_shape.sh`
- `go test ./...`
- `go vet ./...`
- `git diff --check`

The signing test generates an ephemeral SSH key, signs a synthetic archive
attestation, verifies it through a temporary allowed-signers registry, and
confirms unsigned input is rejected. No private key, real account, token,
Matrix server, browser, or external artifact is used.

## Risk

Tag pushes now run a release job that requires the explicitly configured
`CHUZI_RELEASE_SIGNING_KEY` secret and `CHUZI_RELEASE_SIGNER` variable. Missing
or untrusted release notes, tags, signatures, or artifact digests fail closed;
no GitHub Release is created. The signing key is written only to the ephemeral
runner and is removed at job exit. Existing nightly and application behavior is
unchanged.

## Rollback

Disable the stable-release job and continue using nightly artifacts. Revert the
workflow, scripts, release guidance, and CR in a later governance change. No
database, profile, browser, or application state migration is introduced.

## Breaking Change

None to the service, Worker protocol, launcher manifest, account state machine,
or storage schema. Stable releases additionally require release notes and
repository signing inputs.

## Backport Target

none
