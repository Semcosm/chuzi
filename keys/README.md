# Trusted SSH Signers

This directory is the canonical trust registry for SSH-signed commits and
formal release tags in this repository.

Files:

- `allowed_signers`: OpenSSH allowed signers file used for Git and attestation
  signature verification. The release signer uses
  `namespaces="git,ugs-attestation"`.
- `revoked_signers`: OpenSSH revocation file consulted during verification.
- `signer_roles.json`: v0.3 signer roles, fingerprints, status, and effective dates.

Operational rules:

- Add or remove signers only through a topic branch and CR.
- Use the maintainer email address as the signer principal.
- No bot signer is trusted by default.
- Keep at least one standby signing key available when possible.
- If every trusted signing key is lost, use the repository emergency path only
  to rotate trust material and restore signed normal operation.

Signer role records are append-only audit metadata. An active signer MUST be
present in `allowed_signers`; a revoked signer MUST have an effective end date.
The release signer is authorized for both the `git` and `ugs-attestation`
signature namespaces. Attestation signatures must use the latter namespace.
