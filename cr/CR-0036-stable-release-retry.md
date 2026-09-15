# CR-0036: add an auditable stable release retry path

Base: main
Head or Range: be6a9692cb3c2e2c4884316f963f774f1b34b599
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(release): add auditable stable retry publisher
Revision: 2
Status: accepted
Decision: accepted
Policy Version: v0.3
Base OID: be6a9692cb3c2e2c4884316f963f774f1b34b599
Head OID: be6a9692cb3c2e2c4884316f963f774f1b34b599
Integrated Result: pending

## Summary

Add a manual stable-release retry workflow for a release tag whose original
workflow definition cannot publish after the tag is immutable. The workflow
accepts a successful four-target `chuzi-build` run, a signed annotated release
tag, and its full target commit. It validates the source run and retained
Actions artifacts, checks stable manifests and archive contents, signs UGS
attestations, verifies the complete release bundle, and creates the GitHub
Release without replacing the tag.

Extend the existing release artifact validator with an explicit stable channel
and version mode, and document the retry command for operators.

## Motivation

The first `v0.0.1` build produced all four verified target artifacts, but its
stable job used the workflow definition stored in the immutable tag and failed
before it could restore the annotated tag reference. Re-running that tag keeps
using the old definition. A controlled retry entry point is needed to finish
the release while preserving the tag, commit binding, artifact digests, and
signed-release policy.

## Test Evidence

`./scripts/test_build_contract.sh`

`./scripts/validate_action_pinning.sh .ugs/policy.json .github/workflows`

`go test ./...`

`go vet ./...`

`./scripts/test_nightly_artifact_validator.sh`

`./scripts/test_nightly_package.sh`

`GH_REPOSITORY=Semcosm/chuzi scripts/validate_release_retry_run.sh 34930700693 v0.0.1 89ce5af7e9ab3afb0d587a6854ebb6c231cb8935`

The failed original tag run `34930700693` was verified to contain successful
Windows amd64, Linux amd64, Linux arm64, macOS arm64, and aggregate build jobs,
non-expired target artifacts, stable indexes, and stable manifests. Windows and
Linux amd64 artifacts were downloaded locally and passed the stable archive,
resource, index, manifest, and checksum validator.

`git diff --check`

## Risk

The retry workflow has write permission only in its publish job and requires
all release inputs explicitly. It accepts only a completed build run whose
target jobs and aggregate check succeeded, binds every downloaded index and
manifest to the signed tag commit, and fails closed on missing, expired, or
invalid artifacts. The signing key remains a repository secret on the short
lived runner. The existing tag-triggered workflow remains the normal path.

## Rollback

Disable or revert `release-retry.yml`, its validator, and the stable-channel
validator option. No tag, release, service state, or package format migration
is required; an unpublished retry run can simply be cancelled.

## Breaking Change

None. Existing nightly validation defaults to the nightly channel and all
service, launcher, worker, and package contracts remain unchanged.

## Backport Target

none
