# UGS CR Reviewer And Test Attestation Contract v1

## 1. Status and scope

This document defines the optional `ugs-cr-attestation/v1` JSON evidence
object for signed reviewer and test conclusions about a canonical
`ugs-cr/v1` change request. It is deliberately separate from the CR Markdown
record: the CR remains the authoritative change record, while an attestation
is an immutable, independently verifiable claim about that record.

This contract does not replace commit trailers, the v0.3 review gate, or the
`ugs-release-attestation` payload used by the supply-chain profile. A
repository may require both trailer evidence and CR attestations, and a
release may carry both CR evidence and a release attestation. The namespaces,
roles, and bindings in this document must not be conflated with release
attestation semantics.

## 2. Canonical object

An attestation is a JSON document with `format` equal to
`ugs-cr-attestation/v1` and `schema_version` equal to `1`. The schema at
[`.ugs/schema/cr-attestation.schema.json`](../../.ugs/schema/cr-attestation.schema.json)
is authoritative for required fields, types, and the type-specific conclusion
values. Unknown Core fields are invalid. The Core object has exactly these
conceptual fields (values are illustrative):

```json
{
  "format": "ugs-cr-attestation/v1",
  "schema_version": 1,
  "type": "review",
  "repository": "Semcosm/UGS",
  "subject": {
    "cr_id": "CR-0076",
    "revision": 1,
    "binding_sha256": "sha256:<64 lowercase hexadecimal characters>",
    "target_ref": "main",
    "strategy": "rebase-ff",
    "result_oid": "<40 lowercase hexadecimal characters>",
    "scope": "integrated-result"
  },
  "attester": {
    "principal": "reviewer@example.invalid",
    "role": "reviewer"
  },
  "conclusion": "approved",
  "issued_at": "2026-09-25T00:00:00Z",
  "valid_from": "2026-09-25T00:00:00Z",
  "valid_until": null,
  "checks": [
    {"name": "reviewer-policy", "status": "passed"}
  ],
  "signature": {
    "format": "ssh",
    "namespace": "ugs-cr-review",
    "principal": "reviewer@example.invalid",
    "value": "<base64 detached SSH signature>"
  }
}
```

`type` is `review` or `test`. Review conclusions are `approved`, `accepted`,
or `rejected`; test conclusions are `passed` or `failed`. A test attester
MUST NOT claim a review conclusion, and a review attester MUST NOT claim a test
conclusion. A review attester has role `reviewer`; a test attester has role
`tester`. The attester `principal` MUST equal the signature principal.
`issued_at`, `valid_from`, and `valid_until` are RFC 3339 UTC timestamps;
`valid_until: null` means no declared end. The issued timestamp MUST fall
within the validity window.

`checks` is an array of strings or check objects. A check object has a name, a
status of `passed`, `failed`, or `skipped`, and may include an evidence
reference as defined by the schema. Checks are descriptive evidence and do not
weaken the CR validator, commit-signature checks, or supply-chain evidence
validators.

## 3. Binding and lifecycle

The attestation subject MUST match the referenced CR exactly:

- `cr_id`, `revision`, and `binding_sha256` identify the canonical CR model.
  `binding_sha256` is the CR parser's derived `binding.sha256`, including its
  `sha256:` prefix.
- `target_ref` and `strategy` equal the CR integration fields.
- `scope: source` names the CR's declared source identity through the bound
  canonical projection. `scope: integrated-result` additionally names the
  exact result object selected by `Integrated Result`.
- A pending or accepted CR has no integrated result and therefore carries a
  null result OID. An integrated CR MUST carry its target-bound result OID.
  An attestation cannot manufacture an integration result.

An attestation does not silently advance the CR revision. Any change to a
bound CR field, source object, integration strategy, result object,
conclusion, validity window, or signer requires a new signed evidence object.
Existing records remain append-only.

## 4. SSH signature and trust

The signature covers the canonical JSON payload with the `signature` member
removed. Canonicalization recursively sorts object keys, uses compact JSON
separators, emits UTF-8 without ASCII escaping, and preserves array order.
The verifier MUST reject a document whose signature does not verify over that
exact payload.

The namespace is determined by `type` and MUST be:

| Type | SSH namespace |
| --- | --- |
| `review` | `ugs-cr-review` |
| `test` | `ugs-cr-test` |

The namespace is separate from Git commit signing (`git`) and supply-chain
release signing (`ugs-attestation`). The principal must be authorized by the
repository's allowed-signers registry for the namespace and must have a
matching role record. A key rotation adds a new append-only role record with
its effective start date and closes the previous record with an exclusive
`effective_until` boundary; the replacement is valid on that boundary date
and old attestations are not rewritten.

Revocation takes effect on its recorded effective date. An attestation issued
on or after that date is invalid. Evidence issued before the date remains a
historical claim tied to the prior key and timestamp, subject to the
repository's incident/recovery policy; revoking a key does not silently rewrite
CR history. A verifier MUST report a stale or revoked attestation rather than
silently treating it as a current conclusion.

## 5. Rewrite and integration behavior

Rebase, merge, and squash do not preserve an attestation by implication. A
source-scoped attestation remains usable only while the CR revision and all
bound source OIDs remain unchanged. A rebase or regenerated patch series that
changes those OIDs requires a new CR revision or a new attestation explicitly
bound to the new source.

An integrated-result attestation is bound to the exact result OID. A merge or
squash that creates another result object makes the old integrated-result
attestation stale; the replacement result requires a new attestation. A
rebase-fast-forward may reuse evidence only when the result OID and every
other bound value remain identical. Hosting-platform approvals or mutable CI
state are not substitutes for these immutable bindings.

## 6. Compatibility and distribution

The contract is additive to `ugs-cr/v1` and to legacy-v0 historical records.
It does not require rewriting an existing CR to add an attestation. A legacy
record may be attested only when an implementation can derive and preserve the
exact legacy projection and the verifier's policy explicitly permits it.

The bootstrap distribution carries this normative document with the other
`docs/git` specifications. No hosting service or network is required to parse
the payload or verify its detached SSH signature. The repository's existing
`ugs-attestation` release payload, SBOM, build-record, and supply-chain profile
remain unchanged by this contract.
