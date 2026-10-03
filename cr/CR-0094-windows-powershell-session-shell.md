# CR-0094: add fixed PowerShell session shell supervisor

Base: main
Head or Range: 000086c232ed46c07b4a8e493b14e240f845f2c4..ec87e23842d9c7f48a91df3954b56f8515dcb0b4
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add fixed PowerShell session shell supervisor
Revision: 16
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 000086c232ed46c07b4a8e493b14e240f845f2c4
Head OID: ec87e23842d9c7f48a91df3954b56f8515dcb0b4
Integrated Result: pending

## Summary

Add a fixed, signed PowerShell 5.1 supervisor as the managed user's Winlogon
Shell. The service initializes the target user's profile through
CreateProcessWithLogonW(LOGON_WITH_PROFILE), applies the exact Shell value to
that user's SID-derived hive, and retains ownership of the agent, desktop,
worker, lease, and Job Object lifecycle. The Shell reports SID/session-scoped
readiness and waits; it does not launch agents or workers. Windows build,
assembly, manifest, and service package paths now include the script.
The local RDP smoke records its current setup phase and safe exception metadata
from the beginning of account preparation. Profile bootstrap uses the local
machine domain alias and standard Unicode string marshaling for the native logon
API. The session-shell smoke uses APIs available to Windows PowerShell 5.1/.NET
Framework and disposes registry handles before unloading the target hive. The
disposable password exists briefly in managed memory during profile bootstrap;
it is not printed or persisted. Failures are mapped to safe categories without
persisting raw Win32 codes, messages, or credentials.

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
On the Windows workstation, go vet ./..., npm test --prefix browser-worker
(22 tests), PowerShell -ValidateOnly, the targeted environment package test,
and all packages except slotwindows in go test ./... passed. Windows
EvalSymlinks normalized an 8.3 alias; path validation now compares long-path
forms after checking each component for reparse points, with a Windows
short-name regression test. A malformed deny-ACE test fixture was corrected;
the Windows slotwindows rerun and full suite passed. The Windows workstation
then reported a local RDP smoke failure during profile initialization. The
smoke summary exposed only the outer PowerShell exception, so the harness now
records the setup phase, exception metadata, and a safe category mapped from
known native failures. The latest operator query found a Security 4625 event
classified as bad_password with LogonType 2 during profile initialization. A
follow-up run after aligning the local domain alias failed the same way, so the
smoke bootstrap now restores the standard Unicode string marshaling used by an
earlier implementation. The next Windows run reached session-shell policy but
failed there with the hive still loaded and the Shell value absent. Microsoft
API documentation confirms `Path.IsPathFullyQualified` is unavailable on .NET
Framework; the smoke now uses the compatible `IsPathRooted` check, records safe
subphases, and writes through a disposed registry handle. This update remains
pending Windows verification. Authenticode trust, WTS readiness, and native
provisioner smoke remain pending operator verification.

On commit 4abc14d, Windows -ValidateOnly passed, but LocalRdp failed during
session_shell_policy_write_verify after loading the target hive and validating
the command. The PowerShell wrapper discarded the original registry exception.
Revision 7 records the fixed registry operation, unwrapped exception type and
HRESULT, and a safe category without the exception message. Microsoft
documentation lists permission and security exceptions for RegistryKey writes
and documents process-specific registry views; the access behavior remains
unchanged until the specific failure is known. Windows retest is pending.

On commit 336cd5e, -ValidateOnly passed and LocalRdp advanced to
session_shell_policy_hive_unload, but the outer failure still reported an
unavailable operation and did not show the expected unload-failed phase. The
Revision 8 records each reg.exe unload exit code and whether the target hive
remains mounted, and makes unload failure metadata explicit. Microsoft Learn
documents that RegUnLoadKey requires SE_RESTORE_NAME and SE_BACKUP_NAME; this
does not establish the cause because the current path uses reg.exe and its load
step succeeded. Windows verification is pending.

On revision 8, Windows reported unload exit code 0 and confirmed that the hive
was no longer mounted, while the smoke still failed with the stale
session_shell_policy_hive_unload phase. Revision 9 records a post-unload phase,
marks completion of the shell policy call, and captures the PowerShell script
line for the outer failure. This narrows the next run to the actual failing
statement without storing exception messages or paths. Windows verification is
pending.

On revision 9, the captured failure line was the catch block's read of the
registry helper's LastOperation property, so that metadata lookup masked the
original shell registry error. Add-Type definitions persist for the lifetime
of a PowerShell process, while the old helper initializer only checked for the
credential store type. Revision 10 uses a separately versioned registry helper,
loads it even when older helpers are already cached, makes the operation lookup
fail closed to a safe marker, and validates its required members in -ValidateOnly.
Windows verification is pending.

On revision 10, the local RDP smoke disconnected the operator session. Review
found that the harness had moved from the previously verified RDP profile
endpoint to scanning other loopback aliases to avoid Credential Manager
collisions. Revision 11 requires an operator-supplied verified `.rdp` template,
derives the loopback IPv4 endpoint from that file, and rejects non-loopback
addresses and nonstandard ports. The generated smoke profile continues to use
the profile-based mstsc invocation. If the matching Credential Manager target
already exists, the harness retains its native credential record in process
memory, temporarily writes the one-run identity, verifies the saved username,
then restores the original record during normal success or failure cleanup.
Windows `-ValidateOnly` and LocalRdp verification are pending.

On revision 12, the operator confirmed that the verified MiniSession profile
starts RDP without disconnecting the interactive runner. The harness now keeps
the verified transport fields as an internal evidence contract, scans the
loopback aliases for the unique reachable endpoint with an existing
`TERMSRV/127.0.0.x` credential, and generates the run-scoped `.rdp` file in the
smoke directory. The endpoint and disposable Chuzi username remain dynamic;
the generated profile is removed during cleanup. The explicit template path is
retained only as an optional diagnostic override. Windows validation after this
change is pending.

The operator's endpoint scan showed that all loopback aliases report TCP 3389
reachable, while the only saved route evidence is `TERMSRV/127.0.0.2` with a
`LegacyGeneric` Credential Manager record. Revision 13 teaches the versioned
credential helper to read and temporarily replace both Generic and Domain
Password records, preserving the original credential type for restoration.
This keeps endpoint selection evidence-based without embedding the observed
address in the harness.

The next operator run confirmed the saved `TERMSRV/127.0.0.2` username is the
prototype `SEMCOSM\MiniSession`. During the smoke, the temporary credential
override matched the generated user and WTS reported one active session for that
user; the remaining failure was the session-shell readiness event. Revision 14
adds explicit credential-user, WTS-user, and readiness checks with safe debug
booleans, so a mismatch stops before the native smoke continues.

The Windows workstation then verified the credential and RDP route after the
write path was restored to `cmdkey /generic`. With `TERMSRV/127.0.0.2` already
occupied by the operator's prototype credential, the smoke selected the lowest
unoccupied target, `127.0.0.3`, and launched the generated profile through
`mstsc.exe`. The saved credential username matched the disposable user,
Terminal Services reported the same active WTS username, and the run completed
credential cleanup without removing the pre-existing `127.0.0.2` entry. This
`cmdkey /generic` -> Credential Manager -> `mstsc.exe` loopback route is the
only currently verified local-RDP route. The same run stopped only at
`session_shell_ready=False`; shell readiness remains a separate pending gate.

The endpoint allocation rule is now explicit: parse `cmdkey /list`, reserve the
lowest unoccupied loopback alias in the test range, write the disposable Chuzi
credential for that target, and delete it after the run. Existing user-owned
targets remain untouched; an explicitly supplied verified profile still uses the
same save-and-restore guard.

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
The smoke profile bootstrap briefly materializes the generated disposable
password as a managed string for the native logon call; it must remain confined
to process memory and must never enter output or files.

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
