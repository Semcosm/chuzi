# UGS v0.3 Policy And Conformance Profile

## 1. Status and scope

This document defines the v0.3 adoption profile for repositories that declare
`ugs-policy/v0.3` in `.ugs/policy.json`. It adds a machine-readable policy,
portable conformance checks, strict CR evidence, and protected release/ref
enforcement to the v0.2 Git-native Core.

The v0.2 Core documents remain the historical baseline for the Git object
model, commit vocabulary, review concepts, branch profiles, and release
objects. Where this profile defines a stronger machine-checkable requirement,
the v0.3 declaration and this profile govern new v0.3 changes.

## 2. Canonical declaration

The repository **MUST** publish `.ugs/policy.json` with:

- `format` equal to `ugs-policy/v0.3`;
- `schema_version` equal to `1`;
- a `conformance_level` of `baseline`, `standard`, or `high-trust`;
- a declared branch profile and merge strategy;
- protected refs, commit types, review requirements, and automation checks;
- release tag and signature requirements; and
- an explicit `extensions` object for repository-specific extensions.

The schema is published at `.ugs/schema/policy.schema.json`. Unknown fields
are invalid; extensions **MUST** use keys beginning with `x-`.

The level requirements and profile/merge-strategy matrix are defined in
`docs/git/ugs-conformance-levels.md`.

## 3. Conformance evidence

An implementation claiming v0.3 conformance **MUST** validate the policy
manifest, repository declaration, CR records, commit evidence, review
trailers, release tags, and protected-ref updates.

The reference command is:

```bash
scripts/ugs_check.sh --format json
```

The JSON report **MUST** identify the `ugs-conformance/v0.3` format, an overall
result, and each named check with a pass or fail status.

## 4. Change and review evidence

Every archived CR **MUST** include exact base and head commit OIDs, revision,
decision, policy version, integrated result, risk, rollback, and test evidence.
Accepted integrated changes **MUST** remain auditable without relying on
mutable hosting-platform state.

When `Integrated Result` is present, its `main@<commit OID>` **MUST** identify
an existing commit reachable from `main`, and that commit **MUST** descend from
the CR's `Base OID`. The CR's `Head OID` need not be an ancestor of the
integrated result: hosted rebase, squash, and merge integrations may create a
different result object. A hosted rebase result must retain the same canonical
non-CR tree diff from the CR's `Base OID`.

New v0.3 CRs **MUST** declare `Integration Strategy` as `rebase-ff`, `merge`,
or `squash`. A literal rebase-fast-forward result equals `Head OID`; a hosted
rebase result may use a different commit when its canonical non-CR tree diff
from `Base OID` exactly matches the reviewed `Base OID..Head OID` range. A merge
result is a merge commit containing the source head; and a squash result is a
distinct commit that does not contain the source head as an ancestor.
Historical CRs without this field remain valid as grandfathered records.

During the first post-integration main-range check, a `rebase-ff` CR may still
have `Integrated Result: pending` when a hosting platform rewrites the source
commits. The main-range validator may accept this case only when the recorded
`Base OID` equals the previous main tip, the source `Head OID` descends from
that base, and the source and resulting ranges have the same canonical tree
diff after excluding persisted `cr/CR-*.md` metadata. The rewritten range must
contain one pending CR record; unrelated records or content changes remain
failures. A subsequent closure commit may contain only metadata changes to that
CR, advance its revision, set `Status: integrated`, and name the rewritten
result. The named result must already be reachable from the previous main tip.
This is provenance evidence and does not replace commit signature or
review-trailer validation.

The repository's declared review model and sensitive-path acknowledgment
requirements apply to every v0.3 change. Final review and test conclusions
should be represented by commit trailers when the integration path supports
them. The optional `ugs-cr-attestation/v1` contract adds signed, portable
review and test evidence; it does not weaken or replace this trailer gate.

New v0.3 CRs that declare `Review Evidence: trailers` **MUST** have both
`Reviewed-by` and `Tested-by` on the final integrated commit. For rebase-ff this
is the source head; for merge and squash it is the resulting integration
commit. Review conclusions on discarded or superseded source commits do not
alone satisfy this requirement.

## 5. Protected refs and releases

The declared protected refs **MUST** be enforced at the authoritative boundary
where the repository claims that capability. Local hooks provide early
feedback but are not the sole enforcement layer.

Formal release tags **MUST** be signed annotated tags matching
`v<major>.<minor>.<patch>`, point to commits, and have release notes under
`releases/`. Release tags are append-only: they **MUST NOT** be deleted,
replaced, or force-updated.

## 6. Migration and compatibility

The v0.3 profile is not a compatibility promise for v0.2 manifest, command,
report, or validator behavior. Existing accepted v0.2 commits and release
objects remain valid historical evidence and **MUST NOT** be retroactively
invalidated solely because v0.3 adds stronger checks.

Migration records **MUST** identify the old policy, active v0.3 declaration,
trusted-signing boundary, protected refs, and rollback path. The legacy
`REPOSITORY_POLICY.md` declaration remains available for migration comparison
and warning reporting.

Signer lifecycle metadata **MUST** identify each principal's role, key
fingerprint, effective start date, status, and (when closed or revoked) an
exclusive `effective_until` date. Active lifecycle entries **MUST** correspond
to `keys/allowed_signers`.
Reviewer trailers remain attestations bound to the final signed commit. Signed
reviewer and test attestations use the separate, additive
`ugs-cr-attestation/v1` contract; they do not change the v0.3 CR or commit
trailer wire values.

Exception records under `cr/EX-*.md` **MUST** identify the exception type,
authorizer, reason, start and expiry timestamps, event commit, and post-event
review. Bootstrap exceptions are one-time; emergency exceptions are time-bound
and must close with a reachable `main@<OID>` review result.

## 7. Optional profiles and deferred capabilities

Quality, supply-chain, and repository-shape capabilities are separately
specified optional v0.3 profiles in `ugs-quality-profile.md`,
`ugs-supply-chain-profile.md`, and `ugs-repository-shapes.md`. They are
available to repositories that explicitly declare them; their absence does not
invalidate an otherwise conforming v0.3 repository and this profile does not
silently make them mandatory.

Further work remains for stronger cross-binding of production evidence,
portable adapter capability reporting, and the v1.0 compatibility contract.
The signed CR reviewer/test attestation model is specified separately in
`ugs-cr-attestation/v1`; adoption remains opt-in and is not implied by the
v0.3 profile. Supply-chain release attestations continue to use their existing
`ugs-attestation` contract.

## 8. v0.4 compatibility contract

The v0.3 wire values release and merge remain accepted aliases for the
semantic terms release-line and merge-commit. Unknown Core fields remain
invalid, and repository-specific additions remain limited to extensions keys
beginning with x-. The policy-version versus distribution-SemVer boundary,
deprecation classes, and explicit downgrade requirements are defined in the
UGS v0.4 Contract And Compatibility Guide.
