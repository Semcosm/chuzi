# UGS Canonical Change Request Contract v1

## 1. Status and scope

This document defines the portable UGS Change Request (CR) representation for
the v0.5 interoperability work. It gives a CR one canonical Markdown form, a
deterministic data projection, and a digest that can be bound by optional
review or test attestations.

The contract does not change the active policy manifest wire format or replace
the existing Git commit trailers. Signed reviewer and test evidence is defined
by the optional companion `ugs-cr-attestation/v1` contract; that contract is
independent of hosting APIs and does not make attestation publication
mandatory for a v1 CR.

## 2. Persistent representation

A v1 CR is persisted as one repository-relative Markdown file, normally
`cr/CR-<number>-<slug>.md`. The Markdown file is the sole authoritative
record. A pull request, merge request, email thread, JSON export, or rendered
web page is a view of that record and MUST NOT replace it.

A v1 record begins with `Format: ugs-cr/v1` and `Schema Version: 1`. Producers
claiming this contract MUST write the complete v1 layout defined below. The
schema at `.ugs/schema/cr.schema.json` defines the machine-readable constraints
for the resulting projection.

An existing CR without a `Format:` line is a `ugs-cr/legacy-v0` record. Legacy
records remain valid historical evidence under the rules that accepted them,
but they do not claim v1 canonical rendering. The parser can still produce a
derived projection and binding for a legacy record; a later attestation
contract decides whether that legacy form is eligible evidence. An integrated
legacy record MUST NOT be rewritten merely to add v1 metadata. A new v1 record
or a separate reviewed migration record is required when new evidence needs a
v1 canonical record.

## 3. Canonical Markdown layout

The v1 layout consists of one heading, a fixed ordered metadata block, and the
seven fixed sections. The following is the complete order; an implementation
MUST NOT insert an unrecognized Core metadata line or change the section
order.

```text
# CR-0000: Example canonical change request

Format: ugs-cr/v1
Schema Version: 1
Base: main
Head or Range: docs/example
Integration Target: main
Integration Strategy: rebase-ff
Review Evidence: trailers
Title: Example canonical change request
Revision: 1
Status: pending
Decision: pending
Policy Version: v0.3
Base OID: 0123456789012345678901234567890123456789
Head OID: 0123456789012345678901234567890123456789
Integrated Result: pending
Coverage OIDs: none
Extensions: {}

## Summary

Describe the change.

## Motivation

Describe why it is needed.

## Test Evidence

Record the checks and their results.

## Risk

Record the material risk.

## Rollback

Record the recovery procedure.

## Breaking Change

State whether the change breaks consumers.

## Backport Target

State target branches or none.
```

The heading identifier and the `Title` value identify the same CR title.
Metadata values occupy one physical line. Section bodies may contain ordinary
Markdown, but an H2 heading is reserved for the seven fixed sections. No
unrecognized content may appear before the first section or between metadata
lines. Every document uses UTF-8, LF line endings, the blank lines shown above,
and exactly one trailing LF.

`Extensions` is a single-line JSON object. Its canonical form uses sorted keys
and compact JSON separators. Use `{}` when there are no extensions. Extension
keys MUST begin with `x-`; their values are implementation-defined and an
extension MUST NOT change a Core field's meaning.

`Coverage OIDs` is the literal `none` when no additional commits are named.
Otherwise it is a lexically sorted, ASCII-space-delimited list of distinct full
lowercase SHA-1 OIDs. The list is part of the CR binding and MUST NOT be
reordered.

## 4. Required data and provenance

The v1 projection contains these Core groups:

| Group | Required data |
| --- | --- |
| Identity | CR identifier, title, format, schema version, and revision |
| Source | Base, head or range, base OID, head OID, and coverage OIDs |
| Integration | Integration target, strategy, and integrated result |
| Decision | Status, decision, policy version, and review-evidence declaration |
| Record body | Summary, motivation, test evidence, risk, rollback, breaking change, and backport target |
| Extension | The canonical `Extensions` JSON object |

`Integration Target` names the target ref. The canonical UGS template and this
repository use `main`. `Base OID` and `Head OID` are full lowercase
40-character Git object IDs. `Integrated Result` remains `pending` until
integration, or records `<target-ref>@<OID>`. For `rebase-ff`, the result
normally equals `Head OID`; a hosted rebase may record a different result only
when its non-CR tree diff from `Base OID` is exactly equivalent to the reviewed
source range. The selected integration strategy describes the relationship
between the source and final object under the existing rebase-fast-forward,
merge, or squash rules.

When a hosting platform rewrites a `rebase-ff` source series, the first main
range check may use a bounded provenance comparison while the CR remains
pending. The comparison requires the recorded base to equal the previous main
tip and compares the canonical tree diff of the source and resulting ranges;
persisted `cr/CR-*.md` metadata is excluded from that diff because the CR may
be updated after the source Head OID is recorded. The first rewritten range
must contain one pending CR record. A later closure commit may update only that
record, set a higher `Revision`, set `Status: integrated`, and bind the result
to a commit already reachable from the previous main tip. The closure check
does not replace final signature and trailer checks.

The current review declaration remains compatible with v0.3. If `Review
Evidence: trailers` is claimed, the final integrated object carries the
required review and test trailers under the repository policy. The CR model
does not treat a hosting-platform approval, mutable check result, or free-form
comment as an equivalent authoritative conclusion.

The `Status`, `Decision`, and `Integrated Result` fields form one lifecycle
state and MUST use one of these combinations:

| Status | Decision | Integrated Result |
| --- | --- | --- |
| `pending` | `pending` | `pending` |
| `accepted` | `accepted` | `pending` |
| `integrated` | `accepted` | `<target-ref>@<OID>` |
| `rejected` | `rejected` | `pending` |
| `superseded` | `superseded` | `pending` |

`accepted` with a `pending` result is the pre-integration review decision. It
records that the proposed change was accepted for integration; it is not
itself an integrated provenance result. A record used as completed evidence
must later be closed as `integrated` with a target-bound result, or be
explicitly classified as grandfathered under the roadmap's historical
exception rule.

## 5. Canonical projection and binding

An implementation parses v1 Markdown into one deterministic JSON projection.
The projection contains the schema identifier, format, schema version, CR
identity and revision, source binding, integration binding, decision metadata,
coverage OIDs, fixed section map, and extensions. It contains no derived
binding while the digest is calculated.

The parser emits the following derived binding:

```json
{
  "format": "ugs-cr-binding/v1",
  "sha256": "sha256:<64 lowercase hexadecimal characters>"
}
```

The value is SHA-256 over the UTF-8 bytes of the canonical JSON projection.
Object keys are sorted recursively, JSON uses compact `,` and `:` separators,
and non-ASCII characters are emitted without ASCII escaping. The derived
`binding` object is excluded before serialization, which prevents a circular
digest.

There is deliberately no digest metadata line in the CR file. Consumers derive
the same digest from the record and can store or sign that derived value in a
separate immutable evidence object. A change to any bound identity, source,
integration, decision, section, or extension value changes the digest.

## 6. Canonical parsing and rendering

For a valid v1 document, parsing and rendering are lossless: rendering the
parsed model MUST reproduce the exact input bytes. A v1 consumer MUST reject a
record that cannot meet this invariant instead of silently normalizing field
order, whitespace, line endings, section headings, or extension JSON. This
makes the Markdown durable for humans while keeping the projected evidence
stable for independent implementations.

The reference parser is `scripts/cr_model.py`. It exposes the parsed
projection, canonical rendering, and derived binding for validators, adapters,
and fixture consumers. Implementations may use another language, but they MUST
produce the same projection, rendered bytes, and binding for the published
corpus.

## 7. Compatibility and migration

v1 is additive for the adopted v0.3 CR archive. A validator reports whether a
record is v1 or `ugs-cr/legacy-v0`; it MUST NOT silently upgrade a legacy
record. A legacy parser projection can have a derived binding, but only v1
records receive the strict fixed-layout and canonical-rendering guarantee.

An explicit migration may create a v1 representation only after the operator
reviews the source, target, integration result, and resulting digest. The
migration record must preserve the original historical CR and state the
relationship between the old record and the v1 evidence. Existing accepted
history remains append-only.

## 8. Attestation boundary

Signed reviewer and test attestations are defined by the companion
[`ugs-cr-attestation/v1` contract](ugs-cr-attestation.md). An attestation MUST
bind this record's derived `binding.sha256`, CR revision, and the exact source
or integrated-result identity it claims. The attestation contract defines the
payload, SSH namespaces, role and validity-window checks, revocation and
rotation handling, and stale-evidence behavior after a rebase, merge, or
squash.

Attestations are additive evidence. They do not replace the declared
`Review Evidence: trailers` gate, the required `Reviewed-by` and `Tested-by`
trailers on an integrated object, or any supply-chain release-attestation
requirements. A repository may continue to use the CR model without publishing
attestations.

## 9. Distribution requirements

The bootstrap distribution includes this document, the CR schema, the canonical
CR template, and the reference parser. The optional CR attestation contract
and its schema are distributed alongside these documents when the evidence
capability is installed. Offline consumers can therefore create and inspect v1
records and verify detached evidence without a hosting service or network
access. The portable fixture corpus must include valid and invalid v1 records
and compare their projection and binding across implementations.
