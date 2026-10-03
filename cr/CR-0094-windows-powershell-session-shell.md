# CR-0094: add fixed PowerShell session shell supervisor

Base: main
Head or Range: aa7e4b348f5d51bcd4e24f378fb0c5a4231e1d9d
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add fixed PowerShell session shell supervisor
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 000086c232ed46c07b4a8e493b14e240f845f2c4
Head OID: aa7e4b348f5d51bcd4e24f378fb0c5a4231e1d9d
Integrated Result: pending

## Summary

Add a fixed, signed PowerShell 5.1 supervisor as the managed user's Winlogon
Shell. The service initializes the target user's profile through
CreateProcessWithLogonW(LOGON_WITH_PROFILE), applies the exact Shell value to
that user's SID-derived hive, and retains ownership of the agent, desktop,
worker, lease, and Job Object lifecycle. The Shell reports SID/session-scoped
readiness and waits; it does not launch agents or workers. Windows build,
assembly, manifest, and service package paths now include the script.

## Motivation

The Windows pool needs a predictable interactive user shell before the service
starts its isolated slot agent. A fixed signed script and AllSigned policy
avoid arbitrary command text while preserving the existing service-owned
agent and custom desktop boundaries. The native smoke must verify the exact
target HKCU value and readiness before exercising the agent and worker.

## Test Evidence

Passed on Linux: go test ./..., go test -race ./..., go vet ./...,
GOOS=windows GOARCH=amd64 go build ./..., GOOS=windows GOARCH=amd64 go vet
./..., Windows slotwindows test compilation, repository policy/profile/action
validators, build contract, nightly package contract, nightly artifact
validator, npm test --prefix browser-worker (22 tests), and git diff --check.
PowerShell -ValidateOnly, Authenticode trust, WTS readiness, native provisioner
smoke, and local RDP behavior have not run on Windows. Real-machine smoke
remains pending operator assistance.

## Risk

The managed account's default Explorer shell is replaced by the supervisor.
The profile policy and provisioner fail closed if the script is unsigned,
untrusted, writable by the managed user, or outside the fixed runtime path.
Production installation must sign the packaged script with a trusted code
signing identity and distribute its trust chain and publisher trust for
AllSigned execution. A Windows host must verify that AllSigned permits the
trusted script and that its interactive session reaches readiness. Failed
smoke cleanup may leave only the run-marked disposable user or diagnostics for
operator cleanup.

## Rollback

Stop provisioning managed slots, remove or restore the managed user's Winlogon
Shell value to the prior value, then revert this change. Retire test-created
users and runtime data only through the existing ownership-marked cleanup.

## Breaking Change

Managed Windows users start the fixed PowerShell supervisor rather than the
default Explorer shell. The supervisor waits while the service starts the
agent on its isolated slot desktop. No Core or worker protocol changes are
introduced.

## Backport Target

none
