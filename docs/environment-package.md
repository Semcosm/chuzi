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

The Core/Launcher environment control surface accepts only an opaque package
reference, never a filesystem path. Lifecycle gate operations are durable and
idempotent and produce metadata-only audit events. The long-lived service
resolves install and upgrade references below
`<data_dir>/.chuzi/environment-catalog`, then delegates signature, digest, and
tree validation to `environment.Manager`; rollback uses the manager's verified
rollback tree. A missing catalog entry completes the operation with the stable
`package_unavailable` failure classification. The existing maintenance command
remains available for signed local sources outside the Core/Launcher boundary.

The Windows Core payload includes a reserved test package reference,
`chuzi-windows-test-v1`, for environment ID `chuzi/windows-test` and version
`1.0.0`. The first install request constructs the package from five fixed
`browser-worker/src` files in the installed Core payload. The Windows build
copies these files to `CorePayload/browser-worker/src`. Missing or redirected
resources cause the install to fail; the request cannot name an alternate
file or executable. Core generates an Ed25519 key locally, stores only its
public key in the trust file, and uses the private key only to sign the local
test manifest. The signer name is tied to the public key so an interrupted
bootstrap can retry. Existing catalog entries are revalidated before reuse.
This local test signer is not a production signing authority and does not
attest an external publisher.

Logical-mode Core now exposes the environment manager so the test package can
be installed before switching modes. After install, the separate verify,
trust, enable, and health gates produce a ready record in `environment-list`.
Verify rechecks the installed signature and file tree without granting later
gates. Verification or health failures also persist revoked readiness into
the Store projection, so pool admission cannot reuse the previous ready state.
Core pool apply fills the digest and signer from that record when
`require_trusted` is true and rejects an unready or mismatched record.
Unsigned logical fixtures without a matching package record retain their
existing behavior. Windows pool apply always requires `require_trusted` and a
ready signed record; it never uses the logical fixture fallback.
The offline Windows-mode save checks both the persisted Store record and the
installed signed headless runtime again. A ready environment describes package
readiness only; it does not prove a Windows user, WTS session, desktop, or
Agent heartbeat.

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
