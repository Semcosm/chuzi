# CR-0103: stop and repair the retained Windows Core payload during install

Base: main
Head or Range: 71394f55df66f7ada4542f479c18a73e425b6bee
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): stop and repair retained Core payload during install
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: a243db19b91f913c91aa5e4cbcf77297e5069b8f
Head OID: 71394f55df66f7ada4542f479c18a73e425b6bee
Integrated Result: pending

## Summary

Make the Windows installer converge the Core executable retained under
%ProgramData%\chuzi\data with the CorePayload shipped by the installer.
The installer stops an existing Core before replacing the application files,
then installs the current service component from the new manifest before it
launches the UI. The launcher also treats an old Core capability set as
incompatible and can terminate an orphaned Windows Core process. The Windows
lifecycle smoke test probes hello and requires the current diagnostic and
job-pool capabilities.

## Motivation

Users can successfully reinstall the UI while an older Core process and
executable remain in the shared data root. The UI then reports a ready Core
that lacks get_diagnostic_snapshot and current job-pool methods. This is a
component upgrade failure, not a diagnostic capture failure, and the old
installer did not stop or repair the retained Core during installation.

## Test Evidence

~~~text
./scripts/test_build_contract.sh
go test ./internal/launcher ./internal/coretransport ./internal/coreapi
go vet ./internal/launcher ./internal/coretransport ./internal/coreapi
git diff --check
~~~

The Windows GitHub Actions installer job now validates Core health and checks
the hello response for protocol chuzi.core/v1, get_diagnostic_snapshot, and
list_job_pools before accepting the installer artifact.

## Risk

The installer invokes only the launcher lifecycle and component repair commands
against the fixed Chuzi data root. An upgrade stops Core before copying files
and fails visibly if the retained payload cannot be repaired. The Windows
orphan fallback targets the product's unique chuzi.exe image name. User data
is retained; no credentials or runtime files are logged or added to the
package.

## Rollback

Revert the installer script, build smoke assertion, and contract checks. A
previous installer can still be used to restore the earlier behavior.

## Breaking Change

No protocol or data format change. Installation now fails instead of silently
leaving an old Core executable when the repair step cannot complete.

## Backport Target

None.
