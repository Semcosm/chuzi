# CR-0094: add fixed PowerShell session shell supervisor

Base: main
Head or Range: 000086c232ed46c07b4a8e493b14e240f845f2c4..34bd5dbbe44b62c752027118681613b2998c1704
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: feat(windows): add fixed PowerShell session shell supervisor
Revision: 43
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 2400241812dda68ee2000b4e38e935b783de11ef
Head OID: adc938f199faa194425f2c9d89c9782c45a06c79
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

The next Windows attempt found that `Get-Command -CommandType Cmdlet` did not
discover the ScheduledTasks commands, although the task action itself started.
Revision 21 explicitly imports the ScheduledTasks module and checks commands
without filtering by command type. Its SYSTEM runner now starts the test binary
with separate stdout/stderr capture and records only the safe exception type
and HRESULT if process launch fails, so the next run can distinguish launch
failure from a Go test failure. Windows retest remains pending.

On revision 21, Windows -LocalRdp passed RDP identity and session-shell
readiness but failed native provision with the redacted process_start_failed
category. Revision 22 preserves that stable category and adds safe phase
classification for token duplication, desktop access, CreateProcessAsUser,
Job Object assignment, and thread resume, without recording raw Win32 errors,
paths, usernames, or credentials. A Windows classification regression test
and Windows native smoke rerun are pending.

On revision 22, the native smoke classified the failure as
process_start_desktop_impersonation_failed. The desktop setup passed a primary
token from WTSQueryUserToken to SetThreadToken, which requires an impersonation
token. Revision 23 duplicates a scoped TokenImpersonation handle with only
TOKEN_IMPERSONATE access for desktop creation, closes it on return, and keeps
the primary token for CreateProcessAsUser. Raw Win32 errors and identity data
remain redacted. Windows native smoke rerun is pending.

Revision 23 reached the same redacted desktop impersonation category, indicating
that the explicit token duplication path was still rejected on the target
Windows host. Revision 24 uses the native ImpersonateLoggedOnUser boundary,
which accepts the WTS primary token and creates the thread impersonation token
internally. The primary token remains dedicated to CreateProcessAsUser, and no
raw Win32 error or identity data is recorded. Windows native smoke rerun is
pending.

Revision 24 passed the impersonation stage but failed with the redacted desktop
open category. Windows documents that CreateDesktopW accepts only a simple
desktop name and operates on the calling process's current window station; the
service may run in session 0 while the managed user is active in another
session. Revision 25 starts the packaged user agent on the target user's
winsta0\default desktop with a fixed bootstrap flag. The user agent creates
or opens the simple service-derived desktop inside that target session, exits,
and the service then launches the real agent on the managed desktop. Windows
native smoke rerun is pending.

Revision 25 reached the bootstrap call but failed its redacted validation
category. Paths.Desktop intentionally stores the simple service-owned name
(ChuziSlot hash), while the Windows process boundary requires the qualified
winsta0\ChuziSlot hash form. Revision 26 qualifies the name at the bootstrap
call site and leaves the path and ownership contracts unchanged. Windows native
smoke rerun is pending.

The next Windows run reached the token environment boundary and classified the
failure as process_start_environment_failed. A newly-created RDP user's token
can become interactive before userenv.dll has finished materializing its
profile, so CreateEnvironmentBlock is not guaranteed to succeed at that point.
Revision 27 keeps only the approved Windows runtime variables, derives the
target profile directories from the token when available, and falls back to
the filtered system environment without crossing service secrets. Windows
native smoke rerun is pending.

The following Windows run confirmed that the environment stage still failed,
and the new filtering test exposed the same issue: x/sys/windows
UTF16FromString intentionally rejects embedded NUL code units, while a Windows
environment block requires NUL-terminated entries followed by a second NUL.
Revision 28 encodes the block directly with unicode/utf16 and preserves the
required double terminator. The test now constructs the same native block
shape. Windows native smoke rerun is pending.

The next native run passed the environment stage and classified the remaining
failure as create_process_as_user_failed. Revision 29 maps the safe Win32
failure classes (access denied, missing file/path, invalid parameter, bad
executable, missing privilege/environment, invalid token, or unknown) into the
smoke summary without exposing raw error text or codes. The next run will
identify the CreateProcessAsUser boundary failure category.

The next run identified create_process_as_user_access_denied after the
bootstrap desktop was created successfully. CreateDesktopW inherits the parent
window station descriptor when no security attributes are supplied, so the
service identity was not guaranteed desktop access. Revision 30 passes the
current service SID through the fixed bootstrap environment and adds only that
SID's full desktop access to the existing DACL before the bootstrap exits.
Windows native smoke rerun is pending.

The service SID desktop grant did not change the access-denied result. Windows
requires access on both the desktop and its parent interactive window station.
Revision 31 extends the same service-only DACL grant to `WinSta0` in the target
session before creating or opening the managed desktop. Windows native smoke
rerun is pending.

The next Windows run preserved the complete smoke workspace. Runtime and slot
ACLs grant the managed user read/execute or modify access as expected, and the
failure remains CreateProcessAsUser access denied. Revision 32 enables only
SeAssignPrimaryTokenPrivilege and SeIncreaseQuotaPrivilege on the service
process token immediately before the two managed-user process launches. The
native smoke now reports a separate token-privilege stage if the service token
lacks either privilege. Windows native smoke rerun is pending.

Revision 32 enabled both required service privileges, but the target still
returned access denied only when the service supplied the managed desktop in
`STARTUPINFO`. Revision 33 keeps the agent launch on the target user's verified
`winsta0\default`, passes the validated managed desktop into the agent and its
worker environment, and sets that desktop explicitly for worker processes.
This preserves the isolated browser desktop while avoiding the cross-session
service desktop attachment failure. Windows native smoke rerun is pending.

Revision 34 gives the two `CreateProcessAsUserW` call sites distinct safe
failure categories (`bootstrap` versus `agent`) and reports both the generic
boundary and stage marker without exposing native error details. It also asks
`DuplicateTokenEx` for the documented `TOKEN_QUERY`, `TOKEN_DUPLICATE`,
`TOKEN_ASSIGN_PRIMARY`, and adjustment rights explicitly instead of relying on
`MAXIMUM_ALLOWED`. This follows the Microsoft API contract for the primary
token passed to `CreateProcessAsUserW`; Windows native smoke rerun is pending.

Revision 35 authorizes the target user's `WinSta0` and `Default` objects before
the bootstrap launch, while impersonating the verified WTS token. It adds the
target user and service identity to the existing DACLs with the documented
interactive access masks, then reverts impersonation before calling
`CreateProcessAsUserW`. A separate safe desktop-authorization category records
failures before process creation; Windows native smoke rerun is pending.

Revision 36 opens the existing `WinSta0` and `Default` objects with only
`READ_CONTROL | WRITE_DAC` for DACL inspection and replacement, then grants the
full runtime access ACEs separately. This avoids requiring the impersonated
interactive user to hold every desktop operation right merely to update the
security descriptor; Windows native smoke rerun is pending.

Microsoft documents that `OpenWindowStationW` can open only a window station in
the caller's current session. The service runs in session 0, so impersonating
the RDP token cannot make the service-side `WinSta0` lookup target the user's
session. Revision 37 removes that cross-session lookup and starts the bootstrap
with an empty `STARTUPINFO.Desktop`; Windows then applies its process-connection
rules in the target token's session. The bootstrap itself opens `WinSta0`,
creates the managed desktop, and grants the service identity access before it
exits. Windows native smoke rerun is pending.

The Windows rerun showed that an empty `STARTUPINFO.Desktop` still failed at
the bootstrap `CreateProcessAsUserW` boundary with access denied. Microsoft
documents that an explicit desktop requires full access to the interactive
window station and default desktop, while the service cannot inspect those
objects across sessions. Revision 38 leaves the bootstrap desktop handle null
and has the target-session process open `WinSta0`, bind it with
`SetProcessWindowStation`, and then create the service-derived desktop. The
agent launch remains explicitly bound to the managed desktop. It also aligns
the Windows classification regression test with the stage markers now emitted
for bootstrap and agent process failures. Windows native smoke rerun is
pending.

The next Windows rerun still returned access denied at the bootstrap process
boundary. Revision 39 uses the fixed system `cmd.exe` only as the
`CreateProcessAsUserW` image, with `/d /s /c` and the already validated agent
path as its sole command. The user agent remains the process that attaches to
`WinSta0` and creates the managed desktop; no request-supplied shell text or
executable is accepted. This isolates the service process-creation image from
the target runtime file while preserving the session and environment boundary.
Windows native smoke rerun is pending.

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

The follow-up diagnostics localized that gate to runtime ACLs: the runtime
directory granted `BUILTIN\Users` read/execute, but `icacls` showed no ACE on
`session-shell.ps1`, and `Get-AuthenticodeSignature` returned access denied. The
smoke applied `/inheritance:r` recursively with `/T`, disabling inheritance on
each child while directory-only `(OI)(CI)` ACEs did not grant file access.
Revision 17 protects only the runtime root so its constrained read/execute ACEs
inherit to files, then verifies the runtime root and fixed shell script both
carry a read/execute-only `BUILTIN\Users` allow ACE before starting RDP. Windows
verification of the corrected inheritance and shell readiness is pending.

The endpoint allocation rule is now explicit: parse `cmdkey /list`, reserve the
lowest unoccupied loopback alias in the test range, write the disposable Chuzi
credential for that target, and delete it after the run. Existing user-owned
targets remain untouched; an explicitly supplied verified profile still uses the
same save-and-restore guard.

The operator then confirmed `SESSION_SHELL_READY=True`, but the native Go smoke
failed in `prepareLocalRDPTestSession` with `FindSession` returning
`logged_off`. PowerShell had already observed the managed user's active WTS
session. `FindSession` validates the session SID using `WTSQueryUserToken`,
which requires LocalSystem's TCB privilege; the script had been running the Go
test as the interactive administrator. Revision 18 compiles the opted-in test
binary in the administrator context and runs it through a run-scoped temporary
Task Scheduler task as `NT AUTHORITY\SYSTEM`. The task receives only smoke
paths and run identifiers, never the RDP password or Credential Manager data,
and is removed after completion or failure. Revision 19 sets the task limit to
six minutes and the wrapper wait to six minutes and fifteen seconds, leaving a
bounded window to collect the exit status after task termination. Windows
validation of this runner change remains pending.

The first Windows `-ValidateOnly` attempt reported an unmatched `finally` at
the end of `Invoke-NativeSmokeAsSystem`. The outer smoke harness already owns
task cleanup in its `finally`, so revision 20 removes the redundant inner
cleanup block. The full script and embedded SYSTEM runner both pass the
PowerShell parser; Windows 5.1 `-ValidateOnly` and LocalRdp verification remain
pending.

## Latest Windows Run (2026-10-05)

The clean checkout synchronized to integrated head `40a8303`. Windows
`go vet ./...`, `go build ./...`, the 22-test browser-worker suite, and
PowerShell `-ValidateOnly` passed. The full Windows Go test exposed a stale
runtime fixture in `internal/slotagent/process_windows_test.go`: the valid
configuration omitted the now-required managed `WindowsDesktop` value and
failed before exercising the process launcher. Commit `786feff` supplies the
closed `winsta0\\ChuziSlot...` desktop fixture; Windows rerun is required.

The same run's `-LocalRdp` path passed credential matching, WTS username
matching, active session discovery, and `SESSION_SHELL_READY=True`. Native
provisioning then failed at the SYSTEM-to-managed-user
`CreateProcessAsUser` boundary with the redacted classification
`process_start_failed+create_process_as_user_failed+bootstrap_create_process_as_user_failed+create_process_as_user_access_denied`.
This is an unresolved native host permission or policy failure; it is not
evidence that the Windows smoke gate passed. The preserved RDP diagnostics
show the session stages completed before process creation.

Revision 41 changes each RDP event channel in `rdp-diagnostics.log` to one
bounded summary line containing the sample count, unique event IDs, and latest
timestamp. The previous repeated `Format-List` event payloads were too large
for practical failure review; detailed event messages remain available through
the targeted `Get-WinEvent` commands in the operator handoff.

The follow-up Windows query for the same smoke window found Code Integrity
events 3004 and 3089 for Windows Defender's `DefenderSessionHelper.exe`. No
event referenced the chuzi agent, Node runtime, or browser worker, and the
AppLocker executable log had no matching events. This host observation is
therefore recorded as an unrelated exclusion; native acceptance remains
blocked at the SYSTEM `CreateProcessAsUser` access-denied boundary.

Revision 42 follows the Microsoft CreateProcessAsUserW contract: the fixed
desktop bootstrap now specifies `winsta0\default` instead of a null desktop.
The null form inherits the service's session-0 window station, while the
target token must have access to the selected station and desktop. Linux tests,
vet, builds, and Windows cross-compilation passed; native Windows verification
is pending.

Revision 43 adds an opt-in native smoke diagnostic at the process creation
boundary. When the harness explicitly enables it, the preserved run directory
records only the bootstrap or agent stage, safe Win32 failure category, token
type/session metadata, expected SID and session match booleans, the three
relevant LocalSystem privilege states, and whether the duplicated token
supports token-information queries. It does not record paths, usernames,
credentials, SIDs, or raw error messages, and ordinary service launches never
write the file. Linux tests, vet, builds, and Windows cross-compilation passed;
the next native Windows run is required to identify the remaining access
denial boundary.

Revision 44 fixes the LocalSystem scheduled-task runner so the opt-in token
diagnostic path is passed into the test process. The first revision of this
diagnostic recorded the path in the outer summary but did not inherit the
environment variable into the isolated task, so the Windows run correctly
preserved the existing failure category but produced no token file. Linux
tests, vet, builds, and Windows cross-compilation passed; another native run
is required.

Revision 45 removes the diagnostic path from the encoded SYSTEM task
environment. The task already receives the run root, so the Go smoke process
now derives the fixed diagnostic filename from that root. This keeps the
runner below its command-size limit while retaining the opt-in diagnostic.
Linux tests, vet, builds, and Windows cross-compilation passed; another native
run is required.

Revision 46 keeps the WTS primary token as the first process-start choice, but
when the desktop-bound `CreateProcessAsUserW` call returns access denied it
searches only the target Session for a running process whose token has the
exact managed SID, duplicates that token, and retries the fixed bootstrap or
agent launch. The fallback carries no caller-supplied executable, command,
desktop, or environment values; it is a token-source compatibility path for
interactive desktop ACLs. Linux tests, vet, builds, and Windows
cross-compilation passed; native Windows verification is required.

Revision 47 records the native rerun of Revision 46. RDP identity, WTS
session, and session-shell readiness still passed, and the preserved token log
contains two bootstrap entries, showing that the same-SID interactive-process
token retry executed. Both the WTS token and the interactive-process token
were rejected with access denied, so the remaining investigation moves to the
target window-station/desktop DACL, the SYSTEM task Job Object, and host
execution policy events.

Revision 48 keeps the ordinary CreateProcessAsUserW launch as the first
choice, then adds two bounded retries after ERROR_ACCESS_DENIED: one with
CREATE_BREAKAWAY_FROM_JOB, and one using STARTUPINFOEX with a parent handle
for the exact managed user's Windows PowerShell process in the target Session.
The parent search requires the target SID, Session ID, and the fixed system
Windows PowerShell image; no arbitrary process, executable, command, desktop,
or environment is accepted. The parent handle is closed immediately after the
creation call, and production launches do not emit diagnostics. Linux tests,
vet, and Windows cross-compilation passed; native Windows verification is
pending.

Revision 49 preserves the native smoke's redacted diagnostics across the
agent boundary. The target user writes only a stage marker inside the
service-granted slot work directory; the SYSTEM provisioner copies that marker
into the existing native-token diagnostics file and records whether the
created process was still running or had exited when the named-pipe health
probe failed. This prevents a successful native process creation from being
misreported as an RDP/session failure while keeping production launches and
raw startup errors unchanged. Linux tests, vet, builds, browser-worker tests,
and Windows cross-compilation passed; native Windows verification is required.

Revision 50 adds redacted stage-entry markers to the opt-in agent startup
trace. The marker is written before configuration, runtime launcher, lease,
protocol server, and named-pipe setup; the SYSTEM side still copies it only
when the health probe fails. This distinguishes an agent that never reaches
pipe setup from a live agent whose pipe cannot be opened, without recording
paths, identities, credentials, or raw errors. Linux tests, vet, builds, and
Windows cross-compilation passed; native Windows verification is required.

Revision 51 binds the named-pipe DACL to the validated managed-user SID in
addition to the service SID, and records only a bounded pipe-dial failure
class during the opt-in smoke. The service still returns the stable
session-unavailable classification to callers; the preserved native-token log
now distinguishes access denied, missing pipe, busy pipe, and timeout. Linux
tests, vet, builds, browser-worker tests, and Windows cross-compilation passed;
native Windows verification is required.

Revision 52 gives only the first agent health probe a ten-second startup
window; steady-state health checks retain the two-second timeout. This keeps
the startup path tolerant of delayed profile, Defender, or named-pipe
initialization while preserving bounded scheduler health behavior. Linux tests,
vet, builds, browser-worker tests, and Windows cross-compilation passed; native
Windows verification is required.

Revision 53 defensively drains a bounded additional suspend count after the
managed agent process is created through the interactive-parent fallback. The
launch remains fail-closed if the thread cannot be resumed or remains
suspended; no unbounded resume loop is introduced. Linux tests, vet, builds,
and Windows cross-compilation passed; native Windows verification is required.

Revision 54 keeps the ordinary WTS-token, interactive-token, and breakaway
attempts suspended, but starts the validated Windows PowerShell-parent
fallback without CREATE_SUSPENDED. The fallback process is still assigned to
the service-owned kill-on-close Job Object, while the resume path is skipped
only for that explicitly validated unsuspended launch. This addresses the
native symptom where process creation succeeded but the agent remained alive
without entering startup or creating its pipe. Linux tests, vet, builds,
browser-worker tests, and Windows cross-compilation passed; native Windows
verification is required.

Revision 55 adds opt-in, redacted launch-attempt identity tracing to the native
smoke. Each bootstrap and agent CreateProcessAsUser attempt records only its
bounded label, suspended flag, parent/process/thread IDs, session ID, result
class, and image class; the health failure records the observed agent PID,
parent PID, session match, and image class. This binds a successful fallback to
the process later checked for the named pipe without changing production
logging or the stable service error classification. Linux tests, vet, builds,
browser-worker tests, and Windows cross-compilation passed; native Windows
verification is required.

Revision 56 retries only the bounded named-pipe-not-yet-created race during
agent dialing. The retry remains under the caller's existing startup or health
context deadline; access-denied, invalid, busy, and other pipe failures retain
their existing classifications. This allows a process that has reached pipe
setup to finish creating its first listener instance without weakening the
pipe ACL or authentication boundary. Linux tests, vet, builds,
browser-worker tests, and Windows cross-compilation passed; native Windows
verification is required.

Revision 57 retries one agent launch when the process is created successfully
but exits before startup with Windows `STATUS_DLL_INIT_FAILED`
(`0xC0000142`). The failed process is fully cleaned up before the bounded
250-millisecond retry, and all other launch, health, exit, and cleanup errors
retain their existing classifications. Linux tests, vet, builds,
browser-worker tests, and Windows cross-compilation passed; native Windows
verification is required.

Revision 58 fixes the agent version sent through the health protocol. The
previous fixed value contained '/', which violated the wire identifier grammar
and caused a healthy named-pipe response to be rejected as an invalid message.
The shared wire-safe version constant is now used for the provisioned agent and
environment summary, with a regression test for response validation. Linux
tests, vet, builds, browser-worker tests, and Windows cross-compilation passed;
native Windows verification is required.

Revision 59 makes the bounded loader retry use the validated PowerShell-parent
breakaway launch first. The ordinary parent fallback remains available if the
breakaway call is rejected, while the normal first launch path is unchanged.
This addresses processes that are created successfully but inherit a parent Job
Object boundary and exit with STATUS_DLL_INIT_FAILED before Go startup. Linux
tests, vet, builds, browser-worker tests, and Windows cross-compilation passed;
native Windows verification is required.

Revision 60 makes profile ACL verification parse the SDDL ACE fields and
numeric access mask instead of matching one textual hexadecimal spelling. This
accepts the equivalent Windows SDDL form that omits leading zeroes while still
rejecting a deny ACE for the managed SID. Regression coverage covers both
forms. Linux tests, vet, builds, browser-worker tests, and Windows
cross-compilation passed; native Windows verification is required.

Revision 61 keeps the native smoke's profile ACL lifecycle aligned with the
production runner. The smoke now re-grants the account profile immediately
before `StartJob`, after the earlier grant/revoke coverage, and revokes it
again after the worker stops. This prevents the target-user worker launch from
running against a profile whose slot ACL was intentionally removed by the
preceding test. Linux tests and Windows cross-compilation passed; native
Windows verification is required.

Revision 62 adds a bounded loader fallback for the managed agent. If both
direct `CreateProcessAsUser` launches exit with `STATUS_DLL_INIT_FAILED`, the
service starts the same fixed agent through the system `cmd.exe` interpreter
with a service-constructed command line, preserving the target token,
environment, working directory, and kill-on-close Job Object. The interpreter
path and command are validated and cannot be selected by requests. Linux tests
and Windows cross-compilation passed; native Windows verification is required.

Revision 63 allows the agent's worker processes to break away from the
agent-level kill-on-close Job Object before assignment to their own
kill-on-close Job Object. This preserves tree cleanup while avoiding nested Job
assignment rejection during `StartJob`. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 64 adds opt-in, redacted worker-launch diagnostics to the native smoke.
The agent records the bounded stage and stable error class for runtime path
validation, worker Job creation/configuration, pipe setup, `CreateProcess`, Job
assignment, and thread resume in the existing startup trace. Production agents
do not emit this diagnostic, and no paths, identities, command lines, or raw
native errors are recorded. Linux tests, vet, and Windows cross-compilation
passed; native Windows verification is required.

Revision 65 extends that diagnostic to the protocol dispatch boundary. The smoke
now records whether `StartJob` was rejected before a worker process was created.
The records remain opt-in, redacted, and absent from production agents.

Revision 66 records the native smoke client's bounded write, read, response
validation, and failure-mapping stage. This distinguishes a runtime launch
failure from a malformed or mismatched `StartJob` response without exposing raw
wire data or native error text.

Revision 67 routes client-side smoke diagnostics to the service-owned native
token log when the target-user agent startup path is unavailable in the SYSTEM
process environment. Agent-side startup markers keep using the isolated work
path; production processes remain silent.

Revision 68 adds a bounded `CreateProcessWithTokenW` fallback after the
existing `CreateProcessAsUser` and command-interpreter attempts. The fallback
uses only the validated primary or interactive target token, the fixed agent
path, the filtered environment, and the existing breakaway/cleanup fence.
Native smoke launch diagnostics identify this API path separately. Linux tests,
vet, and Windows cross-compilation passed; native Windows verification is
required.

Revision 69 rejects a token fallback that is created outside the target WTS
session, and adds a desktopless `CreateProcessAsUser` attempt before that
fallback. The agent has no window requirement; workers continue to bind their
validated managed desktop explicitly. Linux tests, vet, and Windows
cross-compilation passed; native Windows verification is required.

Revision 70 fixes the smoke worker diagnostic gate. The target session receives
only `CHUZI_AGENT_*` values, so it cannot see the service-side smoke marker; an
explicit validated startup-diagnostics path now enables the agent-side
redacted worker stage record. Production launches remain silent because they
do not provide that path. Linux tests, vet, and Windows cross-compilation
passed; native Windows verification is required.

Revision 71 splits the worker profile validation diagnostic into path-relation,
profile-root chain, and derived-profile chain stages. The previous `profile_path`
marker combined these checks, so a Windows smoke failure could not distinguish
an invalid derived path from an access or reparse-chain failure. The diagnostic
remains opt-in, redacted, and does not record paths or native error details.
Linux tests and Windows cross-compilation passed; native Windows verification
is required.

Revision 72 preserves the stable error class returned by each profile, package,
worker, and work-directory inspection in the redacted startup diagnostic. This
distinguishes access and missing-path failures from the generic runtime-config
category while retaining the existing path and native-message redaction. Linux
tests and Windows cross-compilation passed; native Windows verification is
required.

Revision 73 grants the managed SID only non-inherited traverse and read-
attributes access to the shared `profiles` directory while a profile lease is
active. The derived profile retains its existing modify ACE; both ACEs are
verified and removed together on revoke and cleanup. This allows the agent's
reparse-chain check to inspect its service-owned parent without exposing sibling
profiles or shared data. Linux tests and Windows cross-compilation passed;
native Windows verification is required.

Revision 74 keeps full reparse-chain validation in the SYSTEM-side profile and
runtime ACL boundaries, and makes the managed-user agent recheck only each
authorized leaf. A managed account cannot read attributes on unrelated parent
directories such as the operator's user profile, so repeating that chain check
inside the agent caused `access_denied` before worker creation. Leaf checks still
reject missing, reparse, and wrong-type targets without widening parent ACLs.
Linux tests and Windows cross-compilation passed; native Windows verification
is required.

Revision 75 corrects the shared profile-root ACE mask to include
`FILE_READ_ATTRIBUTES` alongside execute/traverse, read-control, and synchronize
rights. The previous mask allowed traversal but still caused
`GetFileAttributes` to return `access_denied` at the authorized leaf. Linux
tests and Windows cross-compilation passed; native Windows verification is
required.

Revision 76 scopes that mask correction to the profile-root lease ACE. The
existing slot-directory traverse mask remains unchanged, preserving the
`acl_slot_directories` contract; only the shared profile root receives the
additional read-attributes bit required by `GetFileAttributes`. Linux tests and
Windows cross-compilation passed; native Windows verification is required.

Revision 77 records a bounded profile-ACL stage when the disposable smoke
fails during root or derived-profile grant verification. The stage names and
`acl_drift` category contain no paths, SIDs, native codes, or exception text,
and production provisioners remain silent. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 78 accepts the equivalent `FX` and `GX` SDDL forms that Windows may
emit for the profile-root execute/read-control/read-attributes/synchronize
grant. The verifier still requires a non-inherited allow ACE with the exact
effective mask and rejects managed-SID deny ACEs. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 79 classifies the Windows `ERROR_DLL_INIT_FAILED` status as
`dll_init_failed` in opt-in worker-start diagnostics, preserving the redacted
startup boundary while distinguishing this loader failure from unknown errors.
Linux tests and Windows cross-compilation passed; native Windows verification
is required.

Revision 80 splits the worker launch command-line boundary into command,
application, working-directory, desktop, and environment encoding stages. The
diagnostic remains opt-in and records only the stage and stable error class,
without paths or native error text. Linux tests and Windows cross-compilation
passed; native Windows verification is required.

Revision 81 fixes controlled worker environment construction by encoding each
entry before adding the required NUL separators. The previous implementation
passed an already NUL-delimited block to an API that rejects embedded NULs, so
every worker launch failed at `environment_encode`. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 82 records the worker round-trip and client response boundary in the
opt-in smoke diagnostics. This distinguishes worker process/pipe failure from
an invalid handshake envelope without recording payloads, paths, identities, or
native error text. Linux tests and Windows cross-compilation passed; native
Windows verification is required.

Revision 83 classifies worker stdout EOF separately from invalid JSON output in
the opt-in diagnostic. This identifies an early worker process exit without
capturing its stderr or protocol payload. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 84 records a stable worker process-exit class, including the
`dll_init_failed` loader category, after the child handle signals. The smoke
diagnostic still omits raw exit codes, stderr, paths, and protocol payloads.
Linux tests and Windows cross-compilation passed; native Windows verification
is required.

Revision 85 removes `CREATE_NO_WINDOW` from the worker process launch while
retaining redirected standard handles, the validated managed desktop, and the
worker Job Object. This avoids a Node Windows loader failure observed only on
the non-default desktop. Linux tests and Windows cross-compilation passed;
native Windows verification is required.

Revision 86 binds the creating agent thread to the validated service-derived
desktop only around `CreateProcess`, then restores the default desktop while
letting the worker inherit the managed desktop. This avoids requiring Node to
open the non-default desktop explicitly without weakening desktop isolation.
Linux tests and Windows cross-compilation passed; native Windows verification
is required.

Revision 87 opens the validated `WinSta0` station explicitly, selects it for
the agent process, and opens the managed and default desktops by their
station-local names with the minimum thread-desktop access mask. The previous
fully-qualified desktop string was treated as a literal desktop name by
`OpenDesktopW` and could fail with `file_not_found`. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 88 keeps the Node control worker on the verified session default
desktop. A headed browser is still started by the fixed browser launcher on
the validated service-derived desktop from the filtered worker environment.
This avoids making the Node loader depend on a non-default desktop while
preserving the browser desktop boundary. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 89 routes worker stderr to an inherited `NUL` handle instead of the
JSONL stdout pipe. The worker protocol remains stdout-only, so runtime warnings
or loader diagnostics cannot corrupt the first handshake envelope. Linux tests
and Windows cross-compilation passed; native Windows verification is required.

Revision 90 uses a dedicated inherited stderr pipe drained by the agent instead
of merging diagnostics into the worker JSONL stream. This preserves the
protocol boundary while keeping the child standard error handle fully
pipe-compatible on Windows. Linux tests and Windows cross-compilation passed;
native Windows verification is required.

Revision 91 includes the packaged Node executable as argv[0] in the worker
CreateProcess command line. The executable remains separately validated as the
application name, while the command line now matches a normal Node invocation
(`node.exe worker.mjs --stdio`) during Windows runtime initialization. Linux
tests and Windows cross-compilation passed; native Windows verification is
required.

Revision 92 keeps worker stderr outside the JSONL protocol and records only a
bounded byte count, stable classification, truncation flag, and digest in the
opt-in smoke diagnostics. Raw Node output remains discarded and production
agents remain silent. Linux tests and Windows cross-compilation passed; native
Windows verification is required.

Revision 93 adds a bounded, printable-ASCII stderr summary to the opt-in smoke
diagnostics. Whitespace is collapsed and Windows path tokens are replaced with
`<path>` before writing, while raw worker stderr remains discarded and production
agents remain silent. Linux tests and Windows cross-compilation passed; native
Windows verification is required.

Revision 94 grants the disposable managed user traverse/read-execute access on
the smoke run root, without inheritance, so Node can resolve the separately
ACL-protected runtime tree with `lstat`. Worker stderr now classifies Node's
`EPERM`/`operation not permitted` loader failures as permission errors. Linux
tests and Windows cross-compilation passed; native Windows verification is
required.

Revision 95 places local smoke roots without `RUNNER_TEMP` under the machine
temp directory, because an interactive operator's profile temp path may block
the managed user's parent-directory traversal. The agent also performs a
target-user `Lstat` and read-open check before creating Node, so an inaccessible
worker script is reported at a stable startup stage. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 96 keeps the preserved smoke summary in the operator's normal temp
directory even when the disposable runtime root moves to machine temp. This
keeps the documented retrieval command from reading a stale result from an
earlier run. Linux tests and Windows cross-compilation passed; native Windows
verification is required.

Revision 97 fixes the native smoke's expired-lease fixture so its heartbeat and
expiry timestamps remain valid under the slot lease invariant while the expiry
remains in the past. This exercises the intended `slot.ErrLeaseExpired` fence
instead of failing validation first. Linux tests and Windows cross-compilation
passed; native Windows verification is required.

Revision 98 clears the closed worker kind and account fields when the native
smoke reuses a start request for `StopJob` and `Health`. These commands reject
those fields by protocol design; the fixture now reaches the intended stop and
stale-lease assertions. Linux tests and Windows cross-compilation passed;
native Windows verification is required.

Revision 99 validates the complete managed ownership tree before `Retire`
performs session, profile, or control-plane cleanup. An unknown entry now
fails atomically, allowing the operator to remove only that disposable entry
and retry retirement without entering a partially cleaned state. Linux tests
and Windows cross-compilation passed; native Windows verification is required.

Revision 100 records a smoke-only, redacted retirement stage and stable error
class when `Retire` collapses a cleanup failure to its public sentinel. This
keeps the native operator log actionable without exposing paths, identities, or
Win32 error text. Linux tests and Windows cross-compilation passed; native
Windows verification is required.

Revision 101 distinguishes the database and backup ACL fences inside the
redacted retirement diagnostics. Linux tests and Windows cross-compilation
passed; native Windows verification is required.

Revision 102 retries control-plane trustee revocation across explicit and
inherited ACE shapes before reporting ACL drift. Linux tests and Windows
cross-compilation passed; native Windows verification is required.

Revision 103 removes matching control-plane trustee ACEs from the existing
security descriptor and preserves unrelated principals, with unit coverage for
the SDDL filter. Linux tests and Windows cross-compilation passed; native
Windows verification is required.

Revision 104 refreshes the operator smoke summary on every run and records a
passed status with the current run root and diagnostics. This prevents a later
successful native smoke from being confused with a stale failure log. Linux
tests and Windows cross-compilation passed; native Windows verification is
required.

Revision 105 groups the PowerShell concatenations in the passed summary record
so each `key=value` entry remains one line. This keeps the documented targeted
log retrieval commands usable after a successful native smoke. Linux tests and
Windows cross-compilation passed; native Windows verification is required.

Revision 43 records the successful Windows verification at commit `72c025e`.
The full Go suite and vet passed, script validation passed, and the preserved
local-RDP native smoke passed with `status=passed`, `PASS`, zero remaining
smoke users, matching RDP identity, and a ready session shell. The evidence
covers the fixed PowerShell shell and native retirement path; production RDP
broker/authorizer, signed-package end-to-end execution, real adapter work,
restart/power-loss recovery, and default cleanup remain separate gates.

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
