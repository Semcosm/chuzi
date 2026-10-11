# CR-0117: Windows signing and production packaging

Base: main
Head or Range: 18dc1fe603d3b89f557fa55b65a9b3a7d25cea84
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Windows Authenticode signing, trusted payload packaging, and service lifecycle
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 18dc1fe603d3b89f557fa55b65a9b3a7d25cea84
Head OID: 18dc1fe603d3b89f557fa55b65a9b3a7d25cea84
Integrated Result: pending

## Summary

Add an explicit Windows Authenticode policy and signing/validation path for the fixed session shell, Core payload, helper executables, Node runtime, and installer. Package metadata records the signer binding and signing order so release hashes and attestations are computed only after signing. Windows installation uses the fixed service-management entrypoint, protects mutable data and runtime paths with least-privilege ACLs, and stops services before upgrade or uninstall.

## Motivation

The existing Windows package copied executable and script files without a production signature gate. This change makes missing signer configuration, missing timestamp, wrong publisher/thumbprint, missing production Session Broker, unsigned stable metadata, and tampered evidence fail closed. CI receives only signer thumbprint and timestamp configuration names; the certificate and private key remain in the Windows certificate store and no PFX, password, or private key is committed.

## Test Evidence

- `go test ./...` and `go vet ./...` pass.
- `GOOS=windows GOARCH=amd64 go vet ./...` passes; native Windows Authenticode, SCM, ACL, Inno Setup, and WTS tests require the Windows CI environment.
- `scripts/test_nightly_package.sh`, `scripts/test_nightly_artifact_validator.sh`, `scripts/test_windows_signing_contract.sh`, `scripts/test_build_contract.sh`, all UGS profile validators, and `git diff --check` pass.
- The fixed management entrypoint now exposes `UpgradePrepare`, `ConfirmResources`, `Backup`, `Rollback`, and `CleanupBackup`; upgrade preflight checks the bbolt file plus `profiles` and `journal` handles, and failed post-install validation restores the owned runtime/data-component snapshot. Uninstall uses the same preflight before service deletion or data purge.
- The management entrypoint fixes the data root to `%ProgramData%\chuzi\data`, records a SYSTEM-owned `service-ownership.json` marker, rejects reparse/foreign trees, and preserves the previous signed management script for rollback when a new payload cannot execute.
- Stable manifest generation recomputes every production Authenticode evidence hash against the staged post-signing bytes; test/nightly signing remains explicitly `unsigned-development` even when production signer variables exist.
- Production signing configuration names are `CHUZI_AUTHENTICODE_THUMBPRINTS` and `CHUZI_AUTHENTICODE_TIMESTAMP_URL`; no values are recorded here. The production signer is unavailable in this Linux checkout, so stable release assembly fails closed until the Windows certificate store and timestamp endpoint are configured.

- The Inno installer derives stable production mode from its semver build constant and routes stable install and uninstall through the real payload validator; metadata substring matching cannot downgrade a malformed stable payload to unsigned-development behavior.

## Risk

A production build cannot be published without a valid certificate, configured thumbprint allowlist, timestamp, signed production helper set, and Session Broker executable. Windows-only signature-chain, certificate rotation, service registration, ACL, and installer lifecycle behavior remain pending native verification; Linux only proves metadata, hash, and fail-closed contracts. Test/nightly archives are explicitly `unsigned-development` and are not production-ready.

## Native Handoff

On an authorized Windows runner, build and validate the installable artifact with:

~~~powershell
./scripts/build_windows_slint.ps1 -Mode InstallerExe -Configuration Release -OutputDir "$env:RUNNER_TEMP\chuzi-ui" -CorePayloadDir "$env:RUNNER_TEMP\chuzi-core-payload" -PrebuiltBinaryPath "$env:RUNNER_TEMP\chuzi-ui-prebuilt\chuzi-native-windows.exe"
./scripts/validate_windows_payload.ps1 -PayloadDir "$env:RUNNER_TEMP\chuzi-core-payload" -Production -Offline
./scripts/validate_windows_installer.ps1 -InstallerPath "$env:RUNNER_TEMP\chuzi-ui\ChuziSetup.exe" -EvidencePath "$env:RUNNER_TEMP\chuzi-ui\installer-authenticode-signing.json" -Production -Offline
~~~

Each command returns 0 only on success and a nonzero code for a missing signer, invalid signature, metadata or hash mismatch, ACL failure, or lifecycle failure. Session-09/10 acceptance evidence should be a redacted chuzi-windows-session-acceptance/v1 JSON record containing artifact_sha256, version, scenario_id, started_utc, ended_utc, result (passed, failed, or not_run), cleanup (passed, failed, or not_run), and pending_prerequisites; not_run is never a pass. This Linux checkout has not produced that native evidence.

## Rollback

Reject the stable artifact and retain the previous signed release. For an installed machine, run the fixed management script to drain/stop and confirm resources, then preserve or explicitly delete ProgramData data according to the uninstall choice. An interrupted upgrade leaves an owned marker and is recovered by the next `UpgradePrepare` or `UninstallPrepare`; a failed install invokes `Rollback` before returning failure. Restore the previous package only after ownership and active-session cleanup succeeds. Revert the packaging/signing changes through a reviewed UGS change record if source rollback is required.

## Breaking Change

Stable Windows packages now require the production signer, timestamp, publisher binding, signed production helper set, and `chuzi-session-broker.exe`; an unsigned or incomplete stable package is rejected.

## Backport Target

None.
