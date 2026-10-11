# CR-0112: remove Windows smoke RDP password command-line exposure

Base: main
Head or Range: 8253caf9b0907cd9531b531dad27bff0806dc64e
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(smoke): keep RDP passwords out of argv
Revision: 3
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 8253caf9b0907cd9531b531dad27bff0806dc64e
Head OID: 8253caf9b0907cd9531b531dad27bff0806dc64e
Integrated Result: pending

## Summary

Replace the Windows local-RDP smoke credential write path that converted a
SecureString and passed /pass: to cmdkey.exe. The smoke now uses the existing
native Credential Manager boundary through versioned CredWriteW, CredReadW,
and CredDeleteW calls. Profile bootstrap also passes a SecureString into a
short-lived native buffer instead of creating a managed plaintext password
string.

## Motivation

The previous Set-SmokeRdpCredential implementation put the generated smoke
password into a child process argument list. Clearing the PowerShell variable
after Start-Process could not remove that exposure. The smoke must preserve
the operator-verified -LocalRdp login, Profile, WTS, SYSTEM task, and cleanup
behavior while keeping the password inside the current interactive user's
Credential Manager boundary.

## Test Evidence

Signed implementation commit 571bff05f6a2309ac4afde2ce39f96fd251a68e7 changes only
scripts/test_windows_job_pool_smoke.ps1, docs/job-user-pool.md, and
docs/operations.md. The script's -ValidateOnly path now performs a disposable
Credential Manager write/read username match, controlled restore of an existing
target, new-target deletion, active-override collision rejection, and refusal
to restore after an ownership change. Native credential and profile buffers are
released with ZeroFreeGlobalAllocUnicode.

Passed on the current Linux checkout:

./scripts/validate_policy_manifest.sh

./scripts/validate_quality_profile.sh

./scripts/validate_supply_chain_profile.sh

./scripts/validate_action_pinning.sh

./scripts/validate_repository_shape.sh

./scripts/test_build_contract.sh

go test ./...

go vet ./...

git diff --check

Static boundary checks confirmed that the smoke script contains no plaintext
SecureString/BSTR converter, /pass: argument, or Start-Process cmdkey write
path, and that native write/restore and zeroed-buffer calls are present.

No Windows host or PowerShell runtime was available in this environment, so
scripts/test_windows_job_pool_smoke.ps1 -ValidateOnly and the previously
operator-verified -LocalRdp native run were not executed here. Windows
PowerShell parsing/Add-Type compilation, disposable Credential Manager
write/read/delete, process-argv inspection, and end-to-end cleanup remain the
native acceptance evidence for the integration runner.

## Risk

The native helper must compile under Windows PowerShell 5.1 and preserve both
Generic and Domain Password Credential Manager records. Ownership is proved by
the run-scoped target, credential type, and generated username before restore;
if that proof fails, the record is retained and cleanup reports an error. The
native Windows acceptance run is still required to verify these OS behaviors.

## Rollback

Stop the smoke run and revert signed commit f965079. Existing ownership
markers and the prior smoke cleanup path remain the operator's recovery
boundary; do not delete an unverified Credential Manager target manually.

## Breaking Change

None to the service, Core, worker protocol, RDP profile transport settings,
SYSTEM scheduled-task path, or production credential boundary.

## Backport Target

none
