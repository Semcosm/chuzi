# chuzi environment packages

`chuzi-environment/v1` is a signed package metadata boundary. It is distinct
from `chuzi-adapter/v1` and from the Node browser-worker protocol. A package
contains only declared, regular, non-executable resources and closed runtime
entrypoint names. Manifest paths are relative to the package root; symlinks,
reparse or irregular entries, undeclared files, duplicate resources, shell
extensions, and digest/size mismatches fail closed.

The canonical manifest serialization sorts unordered metadata and excludes
signature and digest fields while computing `manifest_digest`. The package
manager verifies the digest, Ed25519 signature, and an explicit signer allowlist
before recording `verified`. Install and `Upgrade` are staged and renamed
atomically. Versions use isolated service-owned roots, so a failed upgrade
leaves the previous version available. The durable lifecycle gates are
independent: `installed`, `verified`, `trusted`,
`enabled`, `healthy`, and `ready`. Trust is never implied by installation and
enabling requires trust. The package manager's staging state survives restart;
the service scheduler consumes the corresponding verified Record persisted through
the Store `environment_packages` API. A changed package loses verification on
the next validation. Interrupted updates can be recovered through the manager's
rollback operation only after both trees pass signature/resource validation;
rollback increments the environment generation and starts untrusted/disabled.

The service-owned control entrypoints are `-environment-install`,
`-environment-upgrade`, `-environment-trust`, `-environment-enable`,
`-environment-disable`, `-environment-health`, `-environment-rollback`, and
`-environment-promote`. Trust keys are loaded from
`<data_dir>/.chuzi/environment-trust.json`; package trees are installed below
`<data_dir>/environments`, and manager state is stored in
`<data_dir>/.chuzi/environment-state.json`. Promotion is explicit and mirrors
the complete manager catalog into bbolt, deleting stale projections. The
Windows service resolves only manifest-declared `worker` or `headless`
entrypoints for the browser worker. Node and `chuzi-user-agent.exe` remain
fixed service-owned binaries; a package cannot select an executable or an
adapter command.

Slots use the persisted environment record as their trusted target when the
lifecycle reconciler is configured with the store authority. Pool requirements
can narrow capabilities but cannot replace the pool environment, version,
digest, signer, or trust requirement. A generation fence rejects late ready
results; deleted slots remain tombstones, and retiring slots are only revived
by an explicit pool scale-up.

Windows provisioning remains behind `slotwindows` and is unavailable on
non-Windows builds. Non-Windows tests use logical slots and fake provisioners;
no test creates users, RDP sessions, named pipes, or system resources. RDP
capabilities remain opaque and are revoked by request, account lease, slot
lease, slot, generation, and service shutdown.
