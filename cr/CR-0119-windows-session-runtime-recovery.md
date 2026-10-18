# CR-0119: integrate Windows session runtime orchestration and recovery

Base: main
Head or Range: d34d4b73cc56f400eac3ece1864a59a101352072
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows session runtime orchestration and recovery
Revision: 34
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 3fbb027a175118e65ef5d645148d516db4ab16ff
Head OID: d34d4b73cc56f400eac3ece1864a59a101352072
Integrated Result: pending

## Summary

Wire the durable Core slot-session operations into the Windows native runtime.
Start, Stop, Cancel, and Retry now create revision- and idempotency-fenced bbolt
operations that an independent service controller claims, renews, checkpoints,
and completes. The controller discovers all durable pools, allows independent
slots and pools to progress concurrently, and prevents the ordinary pool
reconciler from becoming a second owner while an explicit operation is active.

The complete entrypoint chain is:

```text
core.Service Start/Stop/Cancel/RetrySlotSession
-> store durable operation/idempotency/revision
-> independent runSlotSessionController background loop
-> windowsAllPoolReconciler.ReconcileSlotSessionOperations
-> fenced claim/renewal/deadline/cancellation watcher
-> per-pool provisioner UpdatePoolRuntime/Provision/Inspect/StopSession
-> managed identity/Profile
-> brokerHelperBootstrapper
-> private nonce-fenced binary credential handoff
-> fixed session broker
-> local RDP adapter/Credential Manager/mstsc
-> independent WTS verification
-> shell/desktop/Agent/worker readiness
-> redacted durable checkpoint/terminal operation
```

The native provisioner persists service-owned identity and runtime checkpoints,
uses the exact SID/session/generation/owner fence after restart, and resumes
retirement through an external cleanup journal after the slot root has been
removed. The fixed broker persists only credential-free protocol ownership and
tombstones in its authenticated operator state root. Credentials cross the
service/broker process boundary only through the fixed private pipe as a bounded
binary frame with a random nonce and exact ownership fence; both sides clear
their UTF-16 buffers.

Stop retains the managed identity, Profile, and ownership tree for a later
Start. Retire is the destructive lifecycle and deletes only exactly owned
resources through the cleanup journal.

Revision 2 closes four deployment blockers found during review. The interactive
broker no longer calls `WTSQueryUserToken`; the LocalSystem service, with the
documented TCB privilege, performs WTS query/logoff through an authenticated
callback bound to the original SID and exact session fence. Query errors are
classified as errors and never as session absence. A cold Profile is created by
`userenv!CreateProfile`, which is valid for the service identity;
`CreateProcessWithLogonW(LOGON_WITH_PROFILE)` remains only in the operator probe
harness.

Broker protocol, secret frames, and the broker ownership journal are version 2 and include
`pool_id`. A persistent LoopbackAllocator target journal prevents ordinal 1 in
two pools from sharing an RDP target. Missing pool identity in an old record is
rejected rather than adopted. Start/Stop recovery removes an old tombstone only
after a new generation commits ownership. Credential Manager deletion, WTS
query, target journal, or tombstone commit failures retain ownership for retry;
they cannot produce a stopped tombstone. Cleanup runs capability revoke,
Profile revoke, Agent/worker stop, exact session stop, WTS absence, and broker
credential/target cleanup before committing stopped.

CR-0119 belongs to Session 07. Session 08 UI work must receive the next unused
CR number when it begins, after fetching main; it must not reuse CR-0119.

Revision 3 fixes the installer lifecycle observed on a clean uninstall followed
by install. A missing Core service is now an idempotent stop state rather than an
installer error; upgrade preparation uses the elevated service-management path,
and successful installation registers the LocalSystem Core service and the
interactive Session Broker task. Empty fixed data directories left by an
interrupted first install are recoverable, while populated or unexpected data
still fails closed.

Revision 4 fixes the first-install path exposed by the Windows installer smoke
log. PrepareToInstall now performs only the idempotent stop before replacing the
payload; backup and ownership checks remain explicit recovery operations. Service
registration accepts the signed component files and runtime directories that the
installer places in the fixed data root before registration. A Session 0 build
agent registers the interactive Broker task without trying to start it, while an
interactive operator session starts it immediately. The installer smoke check
now fails when Inno reports a script exception despite returning exit code zero.

Revision 5 accepts the fixed top-level `chuzi.db` created by the installed Core
when the service registration resource gate validates the data root.

Revision 6 adjusts the first-install resource gate: populated
signed component directories are accepted, while mutable data directories must
still be empty before ownership is established.

Revision 7 aligns the nightly installer validation path. Authenticode payload
validation is now required only for formal stable versions matching `vX.Y.Z`;
nightly and test artifacts use the unsigned validation path and no longer fail
service registration because the production validator is absent. This is not
evidence for the cause of the preceding test-channel registration failure:
test versions were already exempt, and that failed run did not capture the
PowerShell error output.

Revision 8 records service-management stdout/stderr and exit codes in the Inno
log, so a registration failure can be diagnosed without guessing from the
generic dialog. Uninstall removes the fixed Core service and Broker task before
deleting their management payload, while retaining data by default. Core is
stopped before the Broker so service shutdown can finish session and credential
cleanup. Resource-directory probes use File.Open with CreateNew instead of the
nonexistent File.OpenNew API. Historical Broker-service conversion is removed;
there are no released versions to migrate. A hosted Windows test now verifies
the LocalSystem service command, limited interactive task, uninstall resource
removal, preserved ownership/configuration, and reinstall registration. Its
native Actions result remains pending.

Revision 9 isolates the foreground Core smoke data from the installer's fixed
service data root. Actions run 38568670636 captured
`resource_ownership_unknown` at the first-install top-level resource check:
the preceding foreground test had placed CI-only configuration and log files
in that root. Both foreground tests now use independent runner temporary
directories, including the post-install smoke so it preserves the service data
checked by uninstall/reinstall. The production ownership gate is unchanged.
The same log confirmed that Inno returned zero after the service-management
process returned one; the installer now preserves post-install failure in its
custom exit code. Uninstall cleanup starts only after confirmation, so Cancel
leaves registrations intact. Lifecycle assertions also check the Broker task's
exact executable and absence of arguments. Local build-contract and repository
validators pass; a new exact-commit native installer run remains pending.

Revision 10 fixes the foreground smoke connection after data isolation. Actions
run 38573233504 passed the HTTP health check but reported `core_unavailable`
on hello: the launcher still derived the pipe from the fixed service data root.
The explicitly `coretransport_native_test`-tagged launcher now accepts a native
test data-root environment variable, which the foreground smoke sets only for
its connection and removes in cleanup. Production launchers retain the fixed
root and authenticated service transport. Local launcher/transport tests,
Windows tagged test compilation, build-contract checks, and repository
validators pass. Installed lifecycle and production session acceptance remain
pending.

Revision 11 retains the underlying build-manifest read/parse error in the
installer log and uploads the public source and installed build metadata on
failure. Actions run 38577339010 passed foreground Core smoke but failed
service registration with only `service_build_manifest_invalid`; its Windows
package was not uploaded and the log did not retain the underlying exception.
No resource or signing gate is relaxed. The diagnostic native run and installed
lifecycle acceptance remain pending.

Revision 12 records the final Session 07 review and implementation commit
`9b05e5224146b0b1e9157e2da85c27d782d2803a`. The Store now rejects a runtime
checkpoint after a durable Stop or Cancel intent, including the case where the
controller claim and generation are still valid; the controller immediately
enters exact native cleanup when that fence trips. Boot-only generation-zero
slots may be projected to `stopped` during cleanup, persisted operation
deadlines are converted to a relative context timeout, and launcher consumer
tests use an explicit synthetic install root after command validation. Targeted
Stop/Cancel checkpoint regressions cover both cleanup states and preserve false
readiness facts.

The repository signer registry now records the lost `SHA256:QEc6AA8Qc/tEaLNkvhRjemILH5EVV8S4ogKIQb1cMks`
key as revoked and the replacement `SHA256:C7s8v+W+L7S91DrHDmSd/C8b452NSrSz9NqWpW7jDIY`
key as active. The replacement private key remains outside the repository at
the operator's configured SSH path; GitHub account signing-key registration is
an operator action and does not expose private material to the repository.

Revision 13 records the local Windows package handoff for the rebased head. The
Windows amd64 test artifact `test-20261015-5bdd867c473d` compiled the Go
service, broker, launchers, Node runtime, browser worker, and adapter. The
one-shot local build stopped at its required PresentMon manifest gate until the
existing `prepare_presentmon.ps1` fetched and verified PresentMon 2.6.0 with
SHA-256 `b2a706bc6ad475749e3b7e3409263aa1e6906d45bdcf993f6dbc0f660188f1af`;
the release manifest and component packages then completed.
`validate_nightly_artifact.py --artifact dist --target windows-amd64` passed the
bundle, component archives, adapter, manifest, and checksum evidence. This is a
test package handoff only; the local host has no `cargo`, `rustc`, or Inno Setup
`iscc`, so no Slint/Inno installer EXE is claimed. The native session lifecycle
remains pending for Sessions 09/10.

Revision 15 records signature recovery for the complete PR range. Every commit
from the Session 07 base through implementation and CR handoff was re-signed
with the active registered key; the rewritten commits preserve the original
trees, parent order, authors, and messages. The implementation tree is
unchanged, and its trusted rewritten commit is
`9b05e5224146b0b1e9157e2da85c27d782d2803a`.

Revision 16 records the final package rebuilt from the trusted branch head
`60d39b2712f548c3133c764f05cac50219a85f16`. The bundle is
`dist/chuzi-test-20261015-60d39b2712f5-windows-amd64.zip` with SHA-256
`4004d26d896f298c787cf149af2919f44f291f1d06b4b8a962af3edc4316c627`; its
service component is
`55d69ce613ba79c753c32797dc413baf4bf5999183103a0be9c53649da1435c8`. The
isolated artifact directory passed the complete bundle, component, adapter,
manifest, and checksum validation. This is the package handoff for local
installation; it does not claim Slint/Inno installer or native session
acceptance.

Revision 17 records the Windows installer recovery fix at signed commits
`b9f64e3c456be8dad8f495623f1812df46b9b03b` and
`f52d036c04cfb194fd011b73c54eb7ffdece7535`. The service-management script now
captures native `schtasks.exe` and `sc.exe` results out of process so Windows
PowerShell 5.1 does not turn an absent task or service into a terminating
error. The installer also extracts the management script shipped in the new
package before `PrepareToInstall` stops an existing installation; an older
half-install therefore cannot block an upgrade solely because its stale script
misclassified a missing registration. The local machine had the old
`C:\Program Files\Chuzi` payload but no `ChuziCore` service, no
`ChuziSessionBroker` task, and no Chuzi process. The corrected Stop operation
returned zero for that state.

The rebuilt local installer is
`dist/windows-ui/ChuziSetup.exe`, version
`test-20261016-f52d036c04cf`, SHA-256
`ab22ee723c45ba9e20031035df3f1b03e8ecd99d718756526807bd9c0b721313`.
Inno Setup 6.7.3 compiled it successfully and the embedded Core manifest names
the current commit. This is installer and pre-install recovery evidence only;
the isolated \`validate_nightly_artifact.py\` run also passed the current
windows-amd64 bundle, launcher/service/browser-worker/PresentMon components,
adapter, release index, manifests, and checksums for the same implementation
commit. The local Git Bash policy chain remains environment-limited because
Windows \`jq\` emits CRLF records that the POSIX validators read with a trailing
carriage return; no repository policy result is claimed from that failed host
invocation.
WTS, RDP, Profile, Credential Manager, ACL, Agent/worker, restart, shutdown,
and Retire remain native acceptance pending for Sessions 09/10.

Revision 30 fixes the protected-store failure found while exercising the
installed Windows UI. A standard interactive user could read
`C:\ProgramData\chuzi\data\chuzi.db` but could not open it for writing.
When Core was stopped, the launcher still ran the offline pool-mode maintenance
command and mapped that access failure to `core_stop_required`, which the UI
reported as `core_unavailable`.

Signed implementation `067aa45641c184911e332968f7bff9d241a367ee` adds the
optional `configure_pool_mode` Core API. On Windows the launcher starts the
registered LocalSystem Core when needed, sends the mode request over the
authenticated Core transport, and stops the temporary Core instance after the
service has validated cleanup, revision, signed-environment, and runtime
readiness. The service owns the bbolt open and atomic config write; no
ProgramData ACL is broadened and the operator never calls the store directly.
Invalid and unavailable results remain redacted Core error classes. A focused
service API test covers writing through an open store and rejecting an invalid
mode without changing configuration.

The implementation also includes the already staged Session 09 UI projection
fixes: terminal pool operations are no longer presented as active, logical
capacity cannot claim a Windows session, and the compact inspector close action
receives focus when opened.

At implementation SHA `067aa45641c184911e332968f7bff9d241a367ee`, the Windows
host passed the following checks. Cargo used `VCPKG_ROOT=C:\Tools\vcpkg`,
`VCPKGRS_TRIPLET=x64-windows-static`, and
`LIBCLANG_PATH=C:\Program Files\LLVM\bin` in the PowerShell environment:

- `go test -count=1 ./...`
- `go test -count=1 -race ./...`
- `go vet ./...`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/service`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/session-broker`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/slotwindows`
- `cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`
- `cargo test --manifest-path ui/windows/Cargo.toml --locked` (48 tests)
- `cargo build --manifest-path ui/windows/Cargo.toml --target x86_64-pc-windows-msvc --locked --release`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`

The policy and quality validators were run with a local Git Bash `jq` shim
that removes Windows CRLF from JSON path records; this only compensates for
the Windows jq stdout mode and does not alter repository files. The source
build and cross-compilation checks do not constitute native WTS, RDP, Profile,
Credential Manager, ACL, Agent/worker, restart, shutdown, or Retire acceptance.
Those remain pending for Sessions 09/10. The local installer built from
source commit `a0e4fdadbbedf5b7638e535e829b343570bad184` is
`dist/session-runtime-installer-test-20261018-a0e4fdadbbed/ChuziSetup.exe`,
version `test-20261018-a0e4fdadbbed`, SHA-256
`b421e586555e4bab29c190fb9b193b6a8d9103e14c19692c437bcbdf7d500605`.
The installer payload manifest matches all 16 Core resource hashes and sizes;
the release index and isolated Windows amd64 artifact validator passed. The
installer is ready for operator installation. Its compile and smoke checks do
not claim native session acceptance.

Revision 32 fixes the installed UI failure reproduced while applying the test
pool configuration. Core returned the valid {"operation": {...}} envelope
with terminal state applied, but the UI refresh path attempted to deserialize
the outer envelope as the operation itself. The operation was committed in
Core, then the UI reported invalid_operation_projection and opened the
diagnostic dialog while refreshing the pool list.

Signed implementation 86cc007ea2db617ee24d92fd52cfc7d61e8ef4ce reuses the
same envelope decoder for the refresh projection and adds a regression test for
both terminal and active operation envelopes. The focused and full Rust UI
tests pass (49 passed). A Release Windows build completed with the local
FreeRDP x64-windows-static-md vcpkg dependency, and Inno Setup 6.7.3 produced
dist/session-runtime-installer-test-20261018-pool-projection-fix/ChuziSetup.exe
with SHA-256
8a4d3fac6f140588659bba0ed51a50bc7d0d00618b9dc2c33842b3b11c64e712.
This package is ready for operator installation and retest. The fix only
changes UI projection decoding; WTS, RDP, Profile, Credential Manager, ACL,
Agent/worker, restart, shutdown, and Retire remain native acceptance pending
for Sessions 09/10.

Revision 33 records the review synchronization failure in Actions run
39116193402. The branch was pushed before the PR body was updated from this CR,
so the push-triggered validator compared the previous body with the current CR.
The UI projection fix is signed implementation
`86cc007ea2db617ee24d92fd52cfc7d61e8ef4ce`. The corrected CR body must be
applied to PR #237 before the next push so the validator receives matching
content.

Revision 34 fixes the Drain operation found during the installed Windows
check. Core remained ready and answered launcher queries, while operation
`op_2179e08fc6c4164b` stayed `draining` with `pool-test` configured at
`desired_slots=1` and one retiring slot. The store treated a draining pool as
having zero effective slots, but the lifecycle reconciler still compared the
slot ordinal against the nonzero configured target and never called `Retire`.
Signed implementation `d34d4b73cc56f400eac3ece1864a59a101352072` applies the
zero target consistently for cleanup-only states and adds a regression test
for a draining pool with a nonzero configured target.

The Windows test checks passed: `go test -count=1 ./...`,
`go test -count=1 -race ./...`, `go vet ./...`, Windows amd64 test-binary
compilation for `cmd/service`, `cmd/session-broker`, and `internal/slotwindows`,
`cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`, 49 Rust UI tests,
and a Windows MSVC Release UI build using the installed FreeRDP static triplet.
The policy manifest, quality profile, supply-chain profile, action-pinning,
repository-shape, build-contract, and `git diff --check` validations passed.

Inno Setup 6.7.3 compiled
`dist/windows-session-drain-fix-test-20261018-d34d4b73cc56/ChuziSetup.exe`,
version `test-20261018-d34d4b73cc56`, SHA-256
`4055c7709f163f15bcf57038aed59b926cc9cfd83bbe1bc9a9c560f7497303b0`. Its Core
manifest identifies the full implementation SHA above. This installer is built
for operator installation and has not yet been installed. The currently
installed Core remains healthy but still has the old Drain behavior. Its pool
mode is `logical`, so the UI's `No Windows session` label is expected; native
WTS/RDP/Profile/Credential Manager/ACL/Agent/restart/shutdown/Retire acceptance
remains pending for Sessions 09/10.

Revision 29 records the operator's final UI verification of the replacement package
`test-20261016-9bdf43d1974d`. The operator confirmed that saving Settings
completes without an error and that the saved settings remain correct after
reopening the UI. Core start and stop through the UI also remain functional.
Together with the installed manifest, writable user-scoped settings file, and
the successful deterministic and hosted checks, this closes the Session 07
implementation and UI-state verification scope. Native WTS, RDP, Profile,
Credential Manager, helper ACL/identity, Agent/worker, restart, shutdown, and
Retire acceptance remain explicitly deferred to Sessions 09/10.

Revision 28 records the operator installation of the replacement package
`test-20261016-9bdf43d1974d`. The installed `CorePayload/build-manifest.json`
identifies implementation `9bdf43d1974ddfdebd189463604c0b91874378d1`, and
`ChuziCore` is currently running. The operator reports that Core start/stop
through the UI now works. The user-scoped
`%LOCALAPPDATA%\Chuzi\.chuzi\launcher-settings.json` exists, was created after
installation, and is owned by the interactive user with FullControl. No UI
diagnostics newer than the pre-install failures are present. This confirms the
new user-state location is writable; the operator has confirmed the UI's
`Settings saved.` result and persistence after reopening.

Revision 27 records the UI failure reported with diagnostics
`diag-1792153114170510000-28feea22`,
`diag-1792153117146873500-cfdbda5f`, and
`diag-1792153162099235000-8869e589`. All three came from package
`test-20261016-dc15fcb9534d`, reported Core ready, and captured a generic
`ui_operation_failed`; the visible event entries were idle queue events, and
the truncated lists do not identify the failing UI call. ACL
inspection of `C:\ProgramData\chuzi\data` showed `BUILTIN\Users` has only
ReadAndExecute while SYSTEM has Modify and Administrators have FullControl.
The installed UI was writing user-owned preferences, settings input, launcher
settings, and mutation locks under that service data directory. A standard
interactive user therefore could not save settings or acquire the lock used by
Core start/stop.

Signed implementation `9bdf43d1974ddfdebd189463604c0b91874378d1` moves those
UI-owned files to `%LOCALAPPDATA%\Chuzi` and passes the user-scoped lock path
through to launcher settings save and Core start/stop commands. Existing theme
preferences remain available through a read-only legacy fallback. Focused Rust
path-routing tests and Go launcher tests cover the configured paths. A new
local installer was built for operator installation; UI save/start/stop
verification against the installed package is still pending.

Revision 26 records the installed-package check from diagnostic
`ui-1792152352601`. The installed manifest identifies
`test-20261016-dc15fcb9534d` / implementation
`dc15fcb9534d0afdf3e26b325aa26bf89bfefb61`. At the diagnostic timestamp,
`ChuziCore` was stopped. Installer log `Setup Log 2026-10-16 #007` shows that
setup invoked `core-stop` at 20:05:30, completed service management at
20:05:42, and launched the UI at 20:05:48; the diagnostic was captured at
20:05:52. Thus the diagnostic observed the service stopped after the
installer's explicit stop, before Core was started again. The service log shows
it had reached `ready` at 17:16 local time and emitted `forced_shutdown` at
20:05:22 during that stop path. Starting the registered service through SCM
succeeded. The current process (PID `51300`) reports `RUNNING`, and the
launcher reports `ready: true`, protocol `chuzi.core/v1`, and
`capability_status: supported`; a read-only `list_job_pools` call returned an
empty list. The service was left running.

`forced_shutdown` is emitted when any ordered shutdown hook fails or the
shared five-second shutdown deadline expires. The current event records only
that aggregate result, not the failed hook, so the exact reason for the prior
stop is unknown. This verifies installed Core startup and transport only; WTS,
RDP, Profile, Credential Manager, ACL, Agent/worker, restart, shutdown, and
Retire session acceptance remain pending for Sessions 09/10.

Revision 18 fixes the next installer failure found on the preserved Windows
installation. Setup Log 2026-10-16 #001 records a successful Stop and component
installation, followed by access denied while reading CorePayload's
build-manifest.json. Service creation had not run: ChuziCore and the Broker task
were absent. The installed core-config.json had a protected empty DACL. Running
the old recursive icacls command on owned temporary files under Windows
PowerShell 5.1 reproduced that exact empty-file-DACL/read-denied result.

Signed implementation adc090bfac99c95c6499b52f3cec7a0acd7474fd replaces that
command with protected FileSecurity/DirectorySecurity DACLs, applying inheritance
flags only to directories. SYSTEM and Administrators retain their documented
access and Users receives read/execute only. The script writes only the access
section and preserves file owners. Recursive setup rejects reparse points before
changing the tree. PrepareToInstall uses the current extracted script to recover
only protected empty file DACLs owned by the installer, SYSTEM, or Administrators.
The ownership marker must validate before config or payload recovery; an invalid
marker has its original DACL restored. Other ACL shapes and foreign paths are
not reset. Repair uses the same recovery before resource checks. Failed service
registration also suppresses the post-install UI launch.

The native temporary-file regression covers readable runtime/config files,
exact SID access rules and inheritance, non-recursive behavior, unchanged
owners/content, idempotence, interrupted-install recovery, rejected ownership
mismatch with marker-DACL restoration, and embedded/parent junction rejection.
CI runs it with Windows PowerShell 5.1. This is installer file-ACL evidence only;
the user's installed service and full native session lifecycle remain pending.

The replacement installer is dist/windows-ui/ChuziSetup.exe, version
test-20261016-adc090bfac99, SHA-256
866f4270335cddd0035dc804577d6610b47de2a3ee9d06d34c7ffe25c809cedd. Its payload
manifest records implementation adc090bfac99c95c6499b52f3cec7a0acd7474fd, and
its management script matches the signed source. The preserved installation has
not been uninstalled or had its ACLs changed. The user will run this installer;
repair of that installation and successful service registration remain pending.

Revision 19 fixes the service-registration failure reproduced on the preserved
Windows installation. Windows PowerShell 5.1 was splitting the quoted
`binPath=` and `/TR` values when `sc.exe` or `schtasks.exe` was invoked with an
argument array. The service-management script now serializes every native
argument with Windows command-line quoting, captures stdout/stderr and the
exact exit code, and routes SCM ACL, stop, rollback-delete, and task operations
through that boundary. A dedicated ArgvProbe regression covers spaces, trailing
backslashes, embedded quotes, empty arguments, task commands, and native failure
classification; the hosted Windows package job runs it with PowerShell 5.1.

Signed implementation `818963bb7388f3470e822849e320180f2f42118f` also records
the current local package handoff. The Core/Broker payload and release index
were built as `test-20261016-818963bb7388`; isolated artifact validation passed.
Inno Setup 6.7.3 produced `dist/windows-ui/ChuziSetup.exe` with SHA-256
`368cba21729b1039fa245b4b0e2bae07ad3a61eeaa5fda47bcaad7984d0d8ce6`. The
package reuses the already installed Slint client binary because the local
host has no vcpkg/FreeRDP installation; the UI source build is therefore not
claimed. The user has not installed this new package yet. The preserved
installation still has no `ChuziCore` service or `ChuziSessionBroker` task, and
its latest Setup Log `#003` records the older script's generic
`service_control_failed` exception.

Revision 20 fixes the service startup failure observed after that installation.
The UI's redacted `core_lifecycle` diagnostic reported `core_unavailable`; the
installed `ChuziCore` service was LocalSystem/Automatic but stopped. SCM logged
7009 and 7000 at 13:12:27 on 2026-10-16, while the installed Core manifest still
identified `test-20261016-818963bb7388` and the service executable SHA-256 was
`5c4f58f439754616e4a39bef42d770766ebceea47b1b7bead7aa0086b8b59bbf`. The
installer had registered the service; the failure occurred because the process
performed file, token, and full host preflight before connecting to SCM, beyond
SCM's 30-second startup window. The same host reports a stale Windows 10
ProductName on build 26200 / DisplayVersion 25H2, which the previous edition
gate also rejected.

Signed implementation `6165c9a016e7a2321acc68995383f0080ebb170d` registers the
service with SCM first, sends StartPending checkpoints while fixed runtime
validation runs, and reports Running only after preparation succeeds. It moves
session host preflight into the provisioner after the managed SID is known,
accepts the supported 25H2 stale-ProductName combination, and evaluates local
group SIDs for effective logon rights. Runtime build classification now rejects
non-regular or oversized manifests. Signed build fix
`8a82aca2fbe22c56142842823f97e705d3ce2e87` corrects the service linker symbol
in all three build entrypoints and guards it in the build-contract validator;
the old symbol left the service binary reporting `dev` despite a test manifest.

The replacement installer is
`dist/session-runtime-installer-sourced-ui-test-20261016-8a82aca2fbe2/ChuziSetup.exe`,
SHA-256 `9a7756774e0a608e73b5e8a8c00ffd287c15afddb9ffc369b37f7cc9b0faf503`.
Inno Setup 6.7.3 compiled it successfully. Its build and release manifests
identify implementation `8a82aca2fbe22c56142842823f97e705d3ce2e87`; the service
reports `test-20261016-8a82aca2fbe2`, and all 16 Core resource hashes and sizes
match the bundled files. The Slint UI was built from source with the installed
Rust stable toolchain, Visual Studio 2022 Build Tools, and installed
`freerdp[client]:x64-windows-static`; the build emitted 379 warnings from
generated bindings and existing dead code but succeeded. The installer has not
been installed yet. The existing stopped installation remains on the previous
payload, so service-start recovery and all native Session 09/10 acceptance are
pending. PR #237 remains open; Actions for this implementation are pending push.

Revision 21 records the PR validation synchronization. Push `d42be5f7414c1c53851fb04b8fb43a4ceca93717` triggered UGS run 38864348767 before the updated Revision 20 body had been synchronized, so that event reported `review body differs from CR source`. The current GitHub PR body was then verified byte-for-byte against the committed CR, and `validate_cr_review.sh` passed locally. The UGS workflow uses the default `pull_request` activity types, so editing the body alone did not start another run. This documentation-only revision will synchronize the body before a follow-up push, allowing the next event to validate matching content. It does not change implementation or native acceptance status.

Revision 22 records the native Windows startup failure reproduced after the
`test-20261016-8a82aca2fbe2` installer was installed. The UI diagnostic
`ui-1792134920794` reported `core_unavailable`; the installed `ChuziCore`
service was LocalSystem, Automatic, and configured with an unrestricted service
SID. `sc start ChuziCore` returned successfully and entered `START_PENDING`,
then the process exited and SCM returned the service to `STOPPED`. No new
7000/7009 event, Core log, or `chuzi.db` was created, placing the failure before
store initialization in `ValidateServiceToken`. LocalSystem carried the three
required session privileges in a disabled state, which the previous validator
mistook for a foreign or incomplete token.

Signed implementation
`59114e99bdba0ea737adbc28f2afa99538bfd6fd` now validates privilege presence
independently of the enabled bit and explicitly enables
`SeTcbPrivilege`, `SeAssignPrimaryTokenPrivilege`, and
`SeIncreaseQuotaPrivilege` only after the service SID, LocalSystem identity,
and session-0 checks pass. It preserves the existing fixed identity boundary
and adds focused coverage for disabled-but-present privileges.

Revision 23 records UGS run `38873413113`, which was triggered by the push of
`abb92224231e71e62628972ab53a6096be24c6f4` before the updated PR body was
synchronized and therefore reported `review body differs from CR source`.
The GitHub API body is now byte-for-byte equal to this CR; a follow-up push
will provide the matching event snapshot. The implementation and native
acceptance status are unchanged.

Revision 24 records signed implementation
`dc15fcb9534d0afdf3e26b325aa26bf89bfefb61`. The interactive launcher now opens
SCM and `ChuziCore` with query-only rights for status, adding only the exact
start/stop right for those mutations; the previous `ALL_ACCESS` request made a
healthy LocalSystem service appear as `host_unavailable` to a standard operator.
Core transport now binds the named-pipe server PID to the PID returned by that
authenticated SCM record. If the operator is denied `OpenProcess` on the
LocalSystem service, the fixed service configuration (LocalSystem, Session 0,
and the exact installed executable) remains the authority; a mismatched pipe
PID is still rejected. Focused Windows tests cover query-only controller status
and the PID fence. A local probe observed query status `RUNNING` with service
and pipe PID `10464`; the old launcher path was intentionally rejected by the
Core caller policy because it was outside the installed runtime.

The local Windows test package for this implementation is
`test-20261016-dc15fcb9534d`. Its isolated bundle and component archives passed
release-index/artifact validation. Inno Setup 6.7.3 produced
`dist/session-runtime-installer-test-20261016-dc15fcb9534d/ChuziSetup.exe` with
SHA-256
`aef9b61d1f7ea4463ccd0d3e882a824ccd66fdfabccfd2b8b632f2c52117b03f`.
The package is ready for operator installation; no installed-service handshake
or session lifecycle acceptance is claimed until the new package is installed.

Revision 25 records UGS run `38886708812`. Its Go, Node, and contract jobs
passed, while `ugs-validate` reported `review body differs from CR source`
because the push event was created before the updated Revision 24 body had been
synchronized. The GitHub API body is now byte-for-byte equal to this CR, and
PR run `38886708825` passed all required stages. This revision synchronizes the
body before the next signed push so the next `pull_request` event validates the
same persisted CR. It does not change the implementation or native acceptance
decision.

The current Windows installation still contains the earlier
`test-20261016-59114e99bdba` package. `ChuziCore` is registered and running as
LocalSystem with service and pipe PID `10464`, but that old launcher reports
`core_unavailable`; this is not evidence for the `dc15fcb...` package. The
`test-20261016-dc15fcb9534d` installer remains pending operator installation.
WTS, RDP, Profile, Credential Manager, ACL, Agent/worker, restart, shutdown,
and Retire remain native acceptance pending for Sessions 09/10.

## Motivation

The Core operation APIs, Windows account/Profile/session providers, fixed
broker, and service lifecycle existed as separate boundaries. They did not form
one production assembly that could recover a claimed operation, interrupt a
blocked login after Stop or Cancel, prove readiness independently of the login
provider, or drain sessions during service shutdown. A restart could also lose
process-local Agent and broker maps while durable slot ownership remained.

This change gives the service one durable owner for each operation and one
native ownership fence for each slot generation. Provider results remain
runtime facts; the durable operation state machine decides the externally
visible outcome. Ambiguous users, Profiles, sessions, claims, cleanup records,
and late results fail closed instead of being adopted by SID or pathname alone.

## Runtime State and Recovery

The bbolt operation state and the native ownership journal have different
roles. Operation states are `requested`, `provisioning`, `retrying`, `ready`,
`stopping`, `cancelling`, `stopped`, `cancelled`, and `failed`. A claim owner and
expiry fence one controller; an expired claim can be recovered, while a late
completion from the old owner is rejected. Retry backoff, retry budget, explicit
deadline, action idempotency, revision, desired state, and bounded terminal
history are durable. Referenced operations are not pruned.

The operation phase enum and corresponding recovery behavior are:

| Phase | Durable fact and recovery behavior |
| --- | --- |
| `environment` | Resolve the durable pool and signed runtime. Retry only failures explicitly classified retryable; a disabled, draining, or missing pool fails closed. |
| `host_preflight` | Retain the phase for host-gate reporting. Native host validation remains inside the provisioner boundary and cannot be replaced by a provider success result. |
| `profile` | The native journal binds slot, ordinal, pool, owner, generation, username, and SID before service-compatible CreateProfile and Profile policy continue. Existing ownership is revalidated; unknown or partial ownership is quarantined. |
| `login` | The helper and broker use a bounded context. The public JSON stays credential-free; the one-shot private secret uses the exact ownership plus nonce fence. |
| `wts` | The provisioner independently queries WTS and atomically records the exact active session ID. SID-only discovery never authorizes adoption or cleanup. |
| `desktop` | The fixed managed desktop and signed shell policy remain service-derived. A restart proceeds only from the exact journaled session. |
| `shell` | Winlogon uses the fixed signed PowerShell shell command from the managed Profile. Context failure enters the same fenced cleanup path. |
| `agent` | Agent startup and shutdown are bound to slot, owner, generation, SID, and session. Process-local state is insufficient after restart. |
| `worker` | Provision is followed by an independent Inspect gate. Agent and worker readiness remain false until both provisioned and inspected summaries pass. |
| `stopping` | Revoke capabilities, revoke Profile access, stop the Agent/worker, stop the exact session, verify WTS absence and broker credential/target cleanup. A cleanup failure remains non-terminal and retryable and quarantines an unleased slot. |
| `stopped` | The native session and access grant are absent. The identity, Profile, and ownership tree remain for Start; terminal operation state is committed only after this proof. |

The native slot ownership phases actually written by this implementation are
`intent`, `profile`, `wts`, `agent`, `worker`, `stopping`, and `stopped`.
`desktop`, `shell`, and `unknown` remain valid journal vocabulary for the wider
runtime contract. Cleanup substeps are written after every destructive boundary,
including `begin`, `capabilities_revoked`, `profile_revoked`, `agent_stopped`,
`session_stopped`, `wts_verified`, `control_plane_revoked`, `profile_deleted`,
`root_deleted`, and `user_deleted`.

### Crash Windows

| Crash window | Restart behavior |
| --- | --- |
| Reserved identity, user absent | The derived username is not authority. If the root and user are both absent, Stop is an idempotent no-op and a new provision may begin. Any orphan root, user, or unreadable record fails ownership validation. |
| User created, ownership commit absent | Failure to commit `ownership.json` first writes the external cleanup journal with the exact username and SID, then attempts the compensating user deletion. A surviving account is not adopted on restart; cleanup resumes or the slot is quarantined. |
| Profile half-created | Provision rollback stops any exactly owned session, removes only resources absent at attempt start, and preserves pre-existing resources. An account without matching durable ownership or a malformed/reparse Profile path fails closed. |
| Login complete, session not journaled | The live attempt stops the just-created session if the WTS checkpoint cannot be committed. After a process crash, an active same-SID session without the exact durable session ID is ambiguous and is preserved for quarantine or explicit repair. |
| Agent created, Ready not published | The native `agent` or `worker` checkpoint authorizes restart inspection. Readiness stays false until the independent Inspect result passes; failure, deadline, Stop, or Cancel enters fenced cleanup before a terminal operation is written. |
| Logoff complete, tombstone absent | The service can prove WTS absence from the exact durable session fence and finish `stopped`. The broker journal rechecks WTS on restart and records or accepts a tombstone only for its exact ownership; it cannot adopt a reused session ID. |
| Cleanup failed, then restart | Stop/Cancel remains `stopping` or `cancelling` without `CompletedAt`; the next controller may reclaim it. Retire resumes only from an exact cleanup journal, including the post-root `root_deleted` step, and never generically deletes unknown ownership. |

### Cancellation Boundaries

The operation watcher polls the durable record outside bbolt transactions,
renews the claim every five seconds, and cancels the provider context when the
claim or generation changes or when Stop/Cancel commits the stopped desired
state. The durable owner/generation check rejects a late provider result even if
the provider ignores cancellation.

| Boundary | Cancellation and cleanup behavior |
| --- | --- |
| Credential | Pending secrets are memory-only, expire quickly, and are consumed once. Cancellation drops only the exact ownership and nonce entry; service, helper, adapter, and frame copies are cleared. No password is written to JSON, bbolt, journal, command line, environment, or log. |
| Profile | Context cancellation stops Profile bootstrap or policy work. Rollback removes only a user/root created by the current attempt; an existing or ambiguous Profile is preserved and isolated. |
| Login | Context bounds helper, private-pipe, broker, Credential Manager, and `mstsc` work. Cancellation closes the active request path, deletes the owned credential/profile staging where possible, and routes any established exact session through Stop. |
| WTS | A provider session ID is never sufficient. The provisioner re-queries WTS and requires exact SID, session ID, and active state. A session established before cancellation is stopped only when that fence is available; otherwise cleanup fails closed. |
| Shell | The shell path and command come only from the signed runtime and managed Profile. Cancellation after login uses the journaled session cleanup and waits for WTS disappearance before `stopped`. |
| Agent | Agent shutdown uses the authenticated control pipe and then terminates/closes the owned process tree. Generation, SID, session, and owner mismatches block cleanup. Capabilities are revoked before Agent/session drain. |

Stop and Cancel remain cleanup intents even after their original deadline. A
deadline or exhausted retry budget is retained as the final failure code, but
the operation becomes terminal only after native cleanup succeeds. An active
queue lease defers Stop to lease recovery; shutdown releases a lease only after
the exact owned Agent has been fenced.

## Shutdown and Upgrade Lifecycle

Graceful service shutdown follows this sequence:

```text
servicehost shutdown hook
-> windowsAllPoolReconciler.Shutdown
-> exact durable lease fence and release
-> StopSession for durable slots
-> provisioner Shutdown for in-memory agents, boot-only sessions,
   and journal-discovered sessions
-> close broker helper
```

If an Agent lease fence fails, the durable lease is retained and that slot is
protected from the later StopSession pass. The provisioner then closes known
Agent trees, boot-only sessions, and exact journal-discovered WTS sessions.
Malformed or unknown ownership is left intact and reported as cleanup failure.
The public `StopSession` takes the stable runtime read lock; its internal
`stopSession` path lets shutdown and lease recovery retain that snapshot without
recursively taking the lock while a runtime update writer is waiting.

An upgrade must use the same servicehost shutdown path before replacing the
signed payload. Durable operations, slot ownership, Profiles, broker journal,
and cleanup journal remain available to the next service instance. A forced
shutdown is observable and does not authorize deletion or claim transfer.

## Test Evidence

At implementation SHA `dc15fcb9534d0afdf3e26b325aa26bf89bfefb61`, the Windows
host passed:

- `go test -count=1 ./...`
- `go test -count=1 -race ./...`
- `go vet ./...`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/service`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/session-broker`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/slotwindows`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`
- `git show --show-signature --format=fuller --no-patch HEAD`

The local Windows package was built with Core version
`test-20261016-dc15fcb9534d`. The isolated release-index and test-channel
artifact validation passed for the bundle, launcher, service, browser-worker,
PresentMon, and genshin-cloudgame archives. Inno Setup 6.7.3 produced
`dist/session-runtime-installer-test-20261016-dc15fcb9534d/ChuziSetup.exe`
with SHA-256
`aef9b61d1f7ea4463ccd0d3e882a824ccd66fdfabccfd2b8b632f2c52117b03f`.
The installer reuses the previously verified Slint binary because this local
build used the prebuilt UI input; this is not a new UI source-build claim.
The package is not installed yet, so WTS, RDP, Profile, Credential Manager,
ACL, Agent/worker, restart, shutdown, and Retire remain native acceptance
pending for Sessions 09/10.

At implementation SHA `8a82aca2fbe22c56142842823f97e705d3ce2e87`, this Windows
host passed:

- `go test -count=1 ./...`
- `go test -count=1 -race ./...`
- `go vet ./...`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/service`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/session-broker`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/slotwindows`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`
- `git rebase origin/main` (branch up to date at `3fbb027a175118e65ef5d645148d516db4ab16ff`)

Native Windows tests cover StartPending before slow preparation, Stop during
preparation, runtime build classification, and the machine's
`Windows 10 Pro for Workstations` / build `26200` / `25H2` edition combination.
The service version linker probe and installer manifest/resource hash checks
also passed. `cargo build --manifest-path ui/windows/Cargo.toml --target
x86_64-pc-windows-msvc --locked --release` passed with the initialized MSVC and
vcpkg environment. `npm ci --ignore-scripts` and browser-worker build passed.
Inno Setup 6.7.3 compiled the source-built UI installer above; its sidecar
SHA-256 and all 16 Core resource hashes/sizes match. These checks do not install
the new package or prove WTS, RDP, Profile, Credential Manager, ACL, Agent,
restart, shutdown, or Retire behavior.

At implementation SHA `818963bb7388f3470e822849e320180f2f42118f`, the Windows
host passed `go test -count=1 ./...`, `go test -count=1 -race ./...`,
`go vet ./...`, Windows PowerShell 5.1 `scripts/test_windows_service_acl.ps1`,
and `scripts/test_windows_service_native.ps1`. The three required
`GOOS=windows GOARCH=amd64 go test -c` commands for service, session-broker,
and slotwindows passed. The six policy/build-contract validators and
`git diff --check` passed with the local jq binary wrapper that emits binary
JSON; no validator changes were made. `git rebase origin/main` reported the
branch up to date at base `3fbb027a175118e65ef5d645148d516db4ab16ff`.

The isolated `validate_nightly_artifact.py` run passed the Windows amd64 bundle,
launcher/service/browser-worker/PresentMon components, adapter, release index,
manifests, and checksums for version `test-20261016-818963bb7388` and commit
`818963bb7388f3470e822849e320180f2f42118f`. Inno Setup 6.7.3 compiled the
replacement installer and its sidecar matches SHA-256
`368cba21729b1039fa245b4b0e2bae07ad3a61eeaa5fda47bcaad7984d0d8ce6`. The
Slint source build was attempted and stopped because vcpkg/FreeRDP is absent;
the installer uses the previously installed UI binary as an explicit prebuilt
input. This package handoff and the deterministic checks do not constitute
installed-service or session acceptance.

At implementation adc090bfac99c95c6499b52f3cec7a0acd7474fd, the Windows host
passed go test -count=1 ./..., go test -count=1 -race ./..., go vet ./..., and
windows/amd64 test compilation for cmd/service, cmd/session-broker, and
internal/slotwindows. Windows PowerShell 5.1 passed
scripts/test_windows_service_acl.ps1. All six required policy/build-contract
checks and git diff --check passed. Git Bash used igncr and jq --binary through
an ignored local shell wrapper, resolving the earlier Windows CRLF limitation
without changing repository policy. After fetching origin, rebase onto
3fbb027a175118e65ef5d645148d516db4ab16ff reported the branch up to date; the
native ACL regression and six policy/build-contract checks passed again.
These checks do not constitute installed-service or session acceptance.

Inno Setup 6.7.3 compiled the replacement installer successfully and its SHA-256
sidecar matched. The first build.ps1 invocation stopped at manifest generation
because PresentMon resources were not yet prepared; it is not a passing full
build. After scripts/prepare_presentmon.ps1 verified the pinned dependency,
manifest generation, package assembly, and installer compilation completed.
The final bundle and all component archives, release index, manifests, and
checksums passed:

```text
python scripts/validate_nightly_artifact.py --artifact dist/installer-acl-evidence/artifacts --target windows-amd64 --commit adc090bfac99c95c6499b52f3cec7a0acd7474fd --version test-20261016-adc090bfac99 --channel test
```

All local build artifacts and evidence remain ignored under dist. Exact-commit
Actions for this revision will run after the signed CR update is pushed; the
older revision's runs do not validate this installer fix.

At implementation SHA `9b05e5224146b0b1e9157e2da85c27d782d2803a`, the Windows
checkout passed the following deterministic checks:

- `go test -count=1 ./...`
- `go test -count=1 -race ./...`
- `go vet ./...`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/slotwindows`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/service`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/session-broker`
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`
- `./scripts/validate_signer_roles.sh`
- `git show --show-signature --format=fuller --no-patch HEAD`
- `./scripts/prepare_presentmon.ps1` with the pinned PresentMon SHA-256
- `./scripts/package.ps1 -Target windows-amd64 -Version test-20261015-5bdd867c473d`
- `python scripts/validate_nightly_artifact.py --artifact dist --target windows-amd64 --commit 5bdd867c473daa0849a8220b3051ea5b09e8211f --version test-20261015-5bdd867c473d --channel test`
- `python scripts/validate_nightly_artifact.py --artifact <isolated-dist> --target windows-amd64 --commit 60d39b2712f548c3133c764f05cac50219a85f16 --version test-20261015-60d39b2712f5 --channel test`

`.ugs/document-map.json` is absent, so its optional validator does not apply.

The repository checks were executed from Git Bash with `set -o igncr` because
the Windows checkout uses CRLF and the POSIX validators otherwise pass a
trailing carriage return into path checks. This is a shell compatibility detail;
no repository files were rewritten for it. Go tests and cross-compilation are
not native WTS, RDP, Profile, Credential Manager, ACL, Agent, restart, shutdown,
or Retire acceptance.

Deterministic coverage includes operation idempotency and revision conflicts,
claim expiry and late-result fencing, deadlines, bounded retry and backoff,
cleanup-before-terminal behavior, operation pruning with referenced-history
retention, pool independence during slow login, readiness held false until
Inspect, non-retryable readiness cleanup, Stop/prewarm races, lease-aware
shutdown, and shutdown behavior when a lease fence fails. Broker tests cover
credential-free JSON, narrow ACL declarations, per-slot concurrency, restart
recovery, unknown-session rejection, session-ID reuse, bounded stop, atomic
credential-free journal replacement, malformed private frames, one-shot secret
consumption, exact nonce/ownership drop, and password-buffer clearing. Revision 2
adds cross-pool ordinal-1 target isolation, persistent reservation recovery,
service-mediated WTS callbacks with cancellation, query-vs-absence
classification, cleanup failure without tombstone, new-generation tombstone
replacement, failed ownership commit with failed compensation recovery, and
ordered cleanup with legacy checkpoint recovery.

The Windows runtime test binaries compiled successfully. Hosted Windows Actions
run 38568670636 passed Core named-pipe tests, foreground Core lifecycle smoke,
job-pool preflight, Slint layout checks, and all four target package builds. Its
installed-client smoke failed during service registration with the diagnostic
recorded in Revision 9, so uninstall/reinstall did not execute. The corrected
installer lifecycle still needs a successful native run. Production Broker
login, Credential Manager cleanup, WTS transitions, cold Profile provisioning,
Agent/worker readiness, service restart/recovery, and multi-pool sessions remain
unverified on the user's Windows installation; hosted and compile-only evidence
does not replace that acceptance.

### Sessions 09 and 10 Test Hooks

Session 09 can exercise deterministic recovery without live credentials by
using the store clock, claim owner/TTL, provisioner factory, injected
SessionBootstrapper, readiness gate, cleanup failure, lease-fence failure, and
file broker-journal seams. These hooks cover the seven crash windows as
repeatable process/store restart tests and do not add production fault switches.

Session 10 retains the existing native acceptance entrypoints:
`scripts/test_windows_job_pool_smoke.ps1 -LocalRdp` for the controlled diagnostic
path and `scripts/probes/windows-session-login-probe.ps1` for first login, logout, and
re-login. Production acceptance must additionally run the installed fixed
broker/helper path without the LocalRdp shortcut and prove cold Profile, private
pipe ACLs and caller identity, Credential Manager cleanup, WTS/SID/session
fences, shell/desktop readiness, Agent/worker heartbeat, Stop, service restart,
shutdown, retry, and Retire cleanup on a disposable Windows installation.

At implementation SHA `9bdf43d1974ddfdebd189463604c0b91874378d1`, the Windows
host passed:

- `go test -count=1 ./...`
- `go test -count=1 -race ./...`
- `go vet ./...`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/service`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/session-broker`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/slotwindows`
- `cargo fmt --manifest-path ui/windows/Cargo.toml -- --check`
- `cargo test --manifest-path ui/windows/Cargo.toml --locked` (47 tests)
- `./scripts/validate_policy_manifest.sh`
- `./scripts/validate_quality_profile.sh`
- `./scripts/validate_supply_chain_profile.sh`
- `./scripts/validate_action_pinning.sh`
- `./scripts/validate_repository_shape.sh`
- `./scripts/test_build_contract.sh`
- `git diff --check`
- UI `--smoke-test`

The local installer
`dist/windows-ui-user-state-fix-20261016-9bdf43d1974d/ChuziSetup.exe`
was compiled with Inno Setup 6.7.3. Its SHA-256 is
`8efe3a72a1803dd9b22f8d2b0b8906a3137ce433f733b44c8a54b2023aee4ec9`;
the launcher version and all 16 embedded Core manifest resource hashes were
verified. Build output remains under ignored `dist/`. The package was subsequently installed and its UI settings persistence and Core
start/stop were confirmed by the operator; these checks still do not constitute
native session acceptance.

## Risk

At the latest installed observation, `ChuziCore` was ready, its health probes
passed, and the UI process was responsive. The old installed build continued to
report operation `op_2179e08fc6c4164b` as draining because its reconciler retained
the configured target of one slot. The new Drain fix is in the locally built
installer above and awaits operator installation. Since the current configured
pool mode is `logical`, no Windows session is expected in that mode. No native
session acceptance is claimed.

At Revision 29, the then-installed `test-20261016-9bdf43d1974d` package matched
implementation `9bdf43d1974ddfdebd189463604c0b91874378d1`; `ChuziCore` was
running and the operator reported that UI Core start/stop worked. The user-scoped
settings file existed with an owner FullControl ACL, while the installed service
data root remained read-only for `BUILTIN\Users`. The operator confirmed the UI
Save changes success message and settings persistence after reopening.
Setup Log #007 from the prior installation also recorded `forced_shutdown`,
but it does not identify which
ordered shutdown hook failed or exceeded the five-second deadline. Record the
individual hook outcome before claiming the stop path is reliable.

The empty-file-DACL regression and the Windows PowerShell native argument
splitting failure are reproduced and fixed on owned test inputs. The replacement
installer has been applied to the user's preserved installation and the UI
state and Core lifecycle checks passed.
Recovery intentionally refuses unknown
ownership, nonempty or unprotected file ACLs, and reparse paths; those cases
remain explicit repair failures rather than permission resets. A successful
temporary-file regression does not establish installed-service, helper-pipe
ACL, or production session acceptance.

The main remaining risk is unexecuted native Windows behavior. WTS and RDP
transitions, cold Profile initialization, Credential Manager deletion, helper
identity and named-pipe ACLs, service SID policy, Agent/worker heartbeat and
shutdown, broker/service restart recovery, service shutdown, and Retire cleanup
still require a disposable Windows installation in Sessions 09/10. These can
differ from cross-compiled and fake-provider tests. Until that native
acceptance passes, deployment must report the runtime unavailable and must not
claim production session support.

Cleanup deliberately favors preservation over availability. An ambiguous or
malformed record, active same-SID session without an exact session ID, failed
capability revoke, or failed user/Profile deletion can leave a slot quarantined
for explicit repair. This may reduce pool capacity, but prevents adoption or
deletion of resources whose ownership is not proven.

The broker state root is derived from the authenticated operator SID under the
Windows temporary directory. Deployment ACL or atomic-replace failure makes the
session unavailable. The broker journal is not a substitute for the service
slot journal or bbolt operation record.

## Rollback

Drain scheduler admission and invoke the signed service shutdown path. Preserve
the bbolt store, `job-slots` ownership records, cleanup journal, Profiles, and
broker state root. Confirm that exact leased Agents and owned sessions are
stopped before restoring the previous signed payload. Do not delete an account,
Profile, credential target, session, or slot root whose slot, SID, generation,
owner, session ID, and journal state cannot be verified.

Revert the implementation and this CR through a reviewed change if source
rollback is required. Retained non-terminal operations and ownership records
must be resolved by the compatible recovery implementation or explicit
operator repair; they must not be discarded to make the old binary start.

## Breaking Change

There is no Core wire-format, database-version, or configuration-key break. The
private broker JSON, secret frame, and ownership journal move to version 2 with
a required pool fence. The fixed pipe names keep their v1 suffix. Broker v1
records without `pool_id` fail closed; deployments must complete old-version
Stop, preserve the old journals, and explicitly repair incompatible state
before enabling the new payload. Automatic adoption or deletion is forbidden. On
Windows with the job-user pool enabled, existing Start, Stop, Cancel, and Retry
operations now drive the assembled native runtime and shutdown drains owned
sessions. Deployments therefore need the documented fixed helper registration,
ACLs, privileges, and signed payload before enabling that mode.

## Backport Target

None.
