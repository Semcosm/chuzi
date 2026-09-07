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
integrated result: squash and merge integrations may create a different result
object. Rebase-fast-forward integrations normally use the head object itself.

New v0.3 CRs **MUST** declare `Integration Strategy` as `rebase-ff`, `merge`,
or `squash`. A rebase-fast-forward result equals `Head OID`; a merge result is
a merge commit containing the source head; and a squash result is a distinct
commit that does not contain the source head as an ancestor. Historical CRs
without this field remain valid as grandfathered records.

The repository's declared review model and sensitive-path acknowledgment
requirements apply to every v0.3 change. Final review and test conclusions
should be represented by commit trailers when the integration path supports
them.

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
fingerprint, effective start date, status, and (when revoked) effective end
date. Active lifecycle entries **MUST** correspond to `keys/allowed_signers`.
Reviewer trailers are attestations bound to the final signed commit; v0.3 does
not define a separate reviewer-signature wire format.

Exception records under `cr/EX-*.md` **MUST** identify the exception type,
authorizer, reason, start and expiry timestamps, event commit, and post-event
review. Bootstrap exceptions are one-time; emergency exceptions are time-bound
and must close with a reachable `main@<OID>` review result.

## 7. Deferred capabilities

Quality profiles, supply-chain profiles, and additional repository shapes are
separate follow-up work. They are not implied by the v0.3 profile unless
explicitly declared by a future profile or extension.
