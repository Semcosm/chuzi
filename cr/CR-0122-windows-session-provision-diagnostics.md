# CR-0122: expose safe Windows session provisioning diagnostics

Base: main
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows session provisioning diagnostics for native acceptance
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: pending
Integrated Result: pending

## Summary

The installed Windows session failure was collapsed to the generic
`provision_failed`/`operation_failed` classification, and its Core diagnostic
snapshot did not identify the native provisioning stage or numeric Windows
error. Add a local, bounded diagnostic event with a fixed failure stage and an
optional numeric Win32/HRESULT code. Cover managed-user creation, password
recovery, Profile initialization, session login and WTS verification, Agent,
Profile ACL, and worker checkpoints. Preserve classified failures in diagnostic
snapshots when high-volume background events continue.

The event contains no raw error text, password, SID, username, or filesystem
path. The slot reference is hashed before it is exported. This change adds
diagnostic evidence only; it does not change login, Profile, WTS, or cleanup
behavior.

## Motivation

The durable operation and native logs identify the failed operation and broad
environment phase, but do not expose which native step failed. The operator
needs one installable build that can distinguish password reset, cold Profile
creation, broker login, WTS verification, and later readiness failures before
the underlying behavior is changed.

## Test Evidence

- Windows host: `go test -count=1 ./...`, `go test -count=1 -race ./...`, and
  `go vet ./...` passed.
- Windows UI: `cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`
  passed; `cargo nextest run --manifest-path ui/windows/Cargo.toml --locked`
  passed (50 tests).
- Cross-platform compile: `GOOS=linux GOARCH=amd64 go build ./...` passed.
- Repository validators passed: policy manifest, quality profile, supply-chain
  profile, action pinning, repository shape, build contract, and `git diff
  --check`.
- Local Windows installer build and artifact digest will be recorded after the
  implementation commit is packaged. The package is for operator installation
  and reproduction; it is not native session acceptance.

## Risk

The underlying native failure remains unresolved until a fresh diagnostic is
captured after installation. A missing numeric code means that the failing
boundary did not provide one; the fixed stage and error class remain useful.
Local diagnostic snapshots are user-triggered and remain bounded. No
WTS/RDP/Profile/Credential Manager/ACL/Agent/restart/shutdown/Retire acceptance
is claimed by Go tests or packaging.

## Rollback

Revert the additive diagnostic event fields and provision-failure observer.
They do not modify the durable store schema or ownership records. Keep the
previous installer available until the operator accepts the replacement.

## Breaking Change

No Core API version or store schema change. The local `chuzi.diagnostic/v2`
snapshot gains optional `failure_stage`, `native_error_kind`, and numeric
`native_error_code` fields on events. Existing clients may ignore these
additive fields.

## Backport Target

None.
