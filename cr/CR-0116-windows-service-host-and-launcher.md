# CR-0116: Windows service host, launcher lifecycle, and IPC boundary

Base: main@4b4d4cb4ee93c79e8abe0ed00abfa6acb516a7fe
Head or Range: pending
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows service host, launcher lifecycle, and authenticated IPC boundary
Revision: 6
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 4b4d4cb4ee93c79e8abe0ed00abfa6acb516a7fe
Head OID: pending
Integrated Result: pending

## Summary

Add the fixed Windows SCM service-host boundary, trusted runtime/data-root separation, launcher SCM lifecycle, bounded Core IPC handling, helper registration contracts, host preflight DTOs, and ordered graceful shutdown. Unix keeps its foreground child-process controller.

## Motivation

Production Windows Core must run as the registered `ChuziCore` LocalSystem service with an unrestricted service SID and fixed `-service -config` arguments. A launcher must not execute a writable data-root binary, kill by image name, or treat actor text as OS authentication. Shutdown must drain admission, revoke capabilities, stop owned worker/session resources, and expose forced recovery when its deadline is exceeded.

## Test Evidence

- `go test ./...`, `go vet ./...`, and `git diff --check` pass.
- Windows amd64 test binaries compile for cmd/service, cmd/session-broker, internal/launcher, internal/servicehost, internal/coretransport, internal/slotagent, and internal/slotwindows; the native transport test binary also compiles with coretransport_native_test.
- The repository build, signing, policy, supply-chain, action-pinning, and shape validators pass; Windows packaging now requires the broker executable rather than treating it as optional.
- Unit coverage includes trusted runtime derivation, helper mismatch/staleness, required preflight facts, caller policy, bounded IPC contracts, shutdown ordering, and scheduler admission closure.
- Windows service entrypoint validation now requires the derived ChuziCore service SID, SYSTEM session 0, required privileges, signed runtime, and the exact %ProgramData%\\chuzi\\data\\core-config.json path. Launcher install-root input is checked against its signed executable and mapped to the fixed machine data root. Host preflight rejects unsupported Windows editions, enforces the documented service set and 32-session ceiling, verifies an active RDP listener and machine certificate chain, and reports redacted facts for OS, WTS, SCM, RDP/NLA/certificate, LSA logon rights, effective data access, runtime signature, and helper readiness.
- The Windows transport now derives caller SID/session/executable from the pipe handle, validates PID/token identity in both directions, and wires the authorizer into the service endpoint. The fixed session-broker client also validates the live broker PID and exact installed broker image before accepting a response, closing pipe-squatting/reply-spoofing paths.
- The slot-agent pipe now uses a fixed LocalSystem DACL and authenticates the real service caller from the impersonated pipe token, client PID/token/session, and exact service image before JSON decoding. Agent clients validate the live server PID and exact chuzi-user-agent.exe image; service identity is handed off from the running service executable and no longer selected through caller-controlled SID environment variables. Deterministic authorization tests cover SID, session, authentication, path, and same-name cross-install failures.
- Unix launcher lifecycle keeps PID-validated cross-invocation stop behavior, and scheduler admission closure is bounded by the shutdown context so forced recovery remains explicit when a pass does not return.
- Launcher status now exposes independent host-running, Core-handshake, login-adapter, helper, runtime, and forced-shutdown health dimensions. Helper startup is a fixed-runtime management interface backed by a Windows WTS/CreateProcessAsUser starter: the operator token SID/session is discovered from an authenticated interactive session, the disposable slot SID is never used as the helper identity, and the live process token/image, active WTS session, and fresh heartbeat are revalidated before readiness; stale, crashed, or logged-off helpers are stopped and removed from readiness. Core and broker responses use context-bounded write deadlines, and the Windows broker authenticates the connecting pipe PID/token/image before decoding ownership requests.
- The signed Windows payload now includes a mandatory `chuzi-session-broker.exe`. Installer management registers `ChuziSessionBroker` as an interactive `/SC ONLOGON /IT` task, never a LocalSystem Session 0 service; the broker entrypoint validates its operator token and starts the fixed `SessionBrokerListener.Serve` boundary with fail-closed session operations.
- The servicehost package now publishes the fixed ACL expectation tree for runtime, data, config, credentials, and Profiles, plus an optional effective-DACL validator hook for the Windows preflight/installer handoff. Protected allow lists exclude ordinary Users/Everyone from runtime and mutable-data write roles without using a broad deny ACE that would block slot users through group membership.
- Native SCM, service-token privilege, named-pipe ACL/caller SID, ACL, WTS/helper process creation, and installer upgrade/uninstall checks still require a disposable Windows installation and remain pending.

## Risk

The native disposable-host validation is not available in this Linux checkout. The Core pipe uses a fixed derived name and restricted ACL, the client verifies a SYSTEM-owned chuzi.exe peer, and the server authenticates the caller SID/session/PID/image before applying the fixed launcher policy. Production SCM/ACL/signature behavior still needs Windows evidence before release.

## Rollback

Stop the fixed SCM service through the signed management entrypoint, preserve the ProgramData database, ownership journal, credentials, and Profiles, and restore the previous signed CorePayload only after service-owned resources are confirmed stopped. Revert the source change through a reviewed CR if required.

## Breaking Change

Windows Core lifecycle now requires a compatible `ChuziCore` SCM registration and rejects incompatible service records. Core IPC clients must use the fixed endpoint form and bounded protocol behavior.

## Backport Target

None.
