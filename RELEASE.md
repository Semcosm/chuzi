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
