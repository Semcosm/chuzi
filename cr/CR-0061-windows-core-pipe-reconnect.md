# CR-0061: harden Windows Core pipe reconnects

Base: main
Head or Range: feat/windows-core-release-loop
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: fix(windows): recover Core operations from pipe write races
Revision: 6
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 61c9ad70581eab60c8955d6ab616c7d29158f035
Head OID: ec331a85e816e094a0b85a807546ae4a12140e2e
Integrated Result: pending

## Summary

Harden the Windows native Core client after users reported `pipe hasn't been
connected yet` during Core operations. Readiness probes and API calls use fresh
overlapped named-pipe connections, synchronize the connect state, and batch the
handshake with each API request in one bounded asynchronous write. Failed writes
discard the handle and retry the complete exchange, avoiding both the Windows
overlapped first-write race and synchronous-handle second-write hangs while
keeping the WinUI dispatcher responsive. The Core controller also probes the
derived pipe before trusting process enumeration, preventing a second Core
process from being started when the existing process is elevated or otherwise
hidden from inspection.

## Motivation

`NamedPipeClientStream` can expose a connected state before Windows accepts the
first write on an asynchronous handle. Retrying that handle does not repair its
state, so UI actions such as account lookup and task operations surfaced the
platform exception. Some Windows/go-winio combinations also leave a synchronous
handle blocked on its second write. A fresh per-call overlapped connection with
one batched write and fresh-handle retries avoids both states; bounded read/write
operations keep a broken peer from blocking the UI. Process inspection is not a
reliable readiness signal for an elevated Core process, and using it alone can
cause duplicate starts.

## Test Evidence

Local checks:

`go test ./...`

`go vet ./...`

`./scripts/test_build_contract.sh`

`./scripts/validate_repository_shape.sh`

`git diff --check`

Windows CI must compile the native client and pass the existing Core named-pipe
round-trip and installed-client smoke tests before integration.

## Risk

The change is limited to the Windows native transport and lifecycle status
probe. Writes are bounded by a two-second timeout and the Core frame limit;
timeouts replace the pipe handle before retrying. The bounded status probe adds
a short connection attempt during refresh but avoids long waits when Core is
not running.

## Rollback

Revert the implementation commit. The Core API protocol, data directory, and
persisted state remain unchanged.

## Breaking Change

No.

## Backport Target

none
