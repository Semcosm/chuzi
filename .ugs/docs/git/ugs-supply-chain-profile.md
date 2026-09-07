# UGS Supply-Chain Profile

The optional supply-chain profile declares the strength of a repository's
software provenance evidence without prescribing a language, package manager,
or build service.

```json
{
  "supply_chain": {
    "profile": "basic",
    "action_pinning": "declared",
    "sbom": "declared",
    "reproducible_builds": "declared",
    "release_attestations": "declared"
  }
}
```

`basic` requires every capability to be declared. `standard` requires actions
to use full commit-SHA pinning, a release SBOM, build evidence, and signed
release attestations. `high-trust` additionally requires a verified
reproducible build. The reference validator checks the declared contract;
tool-specific production of SBOMs and attestations remains outside the UGS
Core.

The optional `evidence` object lists repository-relative paths for SBOMs,
attestations, and build records. Paths must not be absolute or escape the
repository. `scripts/validate_supply_chain_evidence.sh` checks the path
contract and profile-specific minimums. `scripts/validate_sbom.sh` accepts
SPDX and CycloneDX documents and requires component identity, version, build
time, source commit, and release metadata.

The profile is additive and optional. Repositories without this section remain
valid v0.3 repositories, and a declaration does not retroactively invalidate
older releases or v0.2 history.

`standard` and `high-trust` declarations MUST include non-empty evidence paths
for an SBOM, build record, and attestation. The JSON Schema expresses these
profile constraints as well as the capability/profile mapping; validators
must enforce the same rules.

Release attestations use `scripts/validate_release_attestation.sh` and bind a
release tag to a commit, artifact SHA-256 digest, builder identity, and build
timestamp. The attestation payload is canonicalized and verified with an SSH
detached signature in the `ugs-attestation` namespace. The release signer is
authorized for that namespace in `keys/allowed_signers`.
`scripts/validate_supply_chain_release.sh` applies these checks to evidence
paths during tag validation.

Build records use `scripts/validate_build_record.sh` and carry the same release
tag, commit SHA, artifact digest, builder identity, and build timestamp. A
release with evidence enabled must keep these identifiers consistent across
the SBOM, build record, and attestation. Release validation also requires an
attestation's repository to match the current Git origin and rejects evidence
for another release or repository.
