# Release Guide

Releases use signed annotated semantic-version tags and the UGS release workflow.

## Nightly builds

Nightly builds are the first release artifact and do not create Git tags or
GitHub Releases. The `chuzi-build` workflow runs at `02:17 UTC` and can also be
started manually. Scheduled and manually started runs use a
`nightly-<run-number>` version and upload one 14-day Actions artifact per
target: Windows amd64, Linux amd64, Linux arm64, and macOS arm64.

Each target bundle contains the complete package, `release-manifest.json`, the
UI-neutral `chuzi-launcher` executable, SHA-256 sidecars, and independently
installable component archives for the launcher, service, browser worker, and
desktop runtime. The manifest is an integrity and capability contract; it does
not imply that a plugin is trusted or that business automation is implemented.

The first launcher command is intentionally non-graphical. It can print and
verify the manifest with `-manifest`, `-root`, and `-verify`. A later UI may use
the same `internal/launcher` interfaces for update checks, repair, component
and plugin management, and behavior settings without changing the package
format.

## Stable signed releases

Stable releases use a signed annotated `v<major>.<minor>.<patch>` tag and a
matching `releases/v<version>.md` file. The tag build uploads all four target
bundles, then the `stable-release` job creates one signed UGS attestation per
archive. Each attestation binds the tag, commit, builder identity, artifact
name, and SHA-256 digest. The job verifies the trusted SSH signature and the
digest against the downloaded artifact before publishing the GitHub Release.

Configure the repository secret `CHUZI_RELEASE_SIGNING_KEY` with the dedicated
release SSH private key and the repository variable `CHUZI_RELEASE_SIGNER`
with its principal. The private key is written only to the ephemeral runner;
it is never committed or included in a release asset. Local verification can
use `scripts/verify_release_bundle.sh` with the repository trust registry.
