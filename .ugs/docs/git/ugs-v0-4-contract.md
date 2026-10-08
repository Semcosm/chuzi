# UGS v0.4 Contract And Compatibility Guide

## 1. Status and scope

This document defines the compatibility contract for the UGS policy wire
format and its machine-readable vocabulary. It is the first v0.4 contract
document and applies to implementations that claim support for the v0.4
compatibility rules.

The document does not change the accepted v0.3 manifest values by itself.
The v0.3 values remain valid for existing consumers; this contract defines
how those values are named, interpreted, extended, deprecated, and migrated.

## 2. Canonical vocabulary

UGS uses semantic terms in normative prose and stable wire values in policy
manifests. A wire alias is accepted for compatibility, but new documentation,
examples, and user-facing diagnostics SHOULD use the canonical term.

| Semantic concept | Canonical term | v0.3 wire value | Compatibility class |
| --- | --- | --- | --- |
| Branch topology with maintained release lines | release-line | release in branching.profile | Alias retained |
| Integration that creates a merge commit | merge-commit | merge in branching.merge_strategy | Alias retained |
| Single active development line | continuous | continuous in branching.profile | Stable |
| Linear fast-forward integration | rebase-ff | rebase-ff in branching.merge_strategy | Stable |
| Squashed integration | squash | squash in branching.merge_strategy | Stable |

release as a policy value means release-line; it does not mean a release tag,
release packet, or release branch. merge as a policy value means merge-commit;
it does not change the requirement that the resulting merge commit and its
evidence satisfy the declared conformance level.

Implementations MUST NOT infer a different branch profile or integration
strategy from human prose when the manifest contains a supported wire value.
When rendering a manifest, implementations SHOULD display the canonical term
and MAY include the accepted wire alias for traceability.

## 3. Policy version and distribution SemVer

policy_version identifies the UGS policy wire contract. It is independent of
the implementation or bootstrap distribution version represented by a signed
SemVer tag such as v0.3.28.

The following rules apply:

1. A patch distribution release MAY correct implementation defects,
   diagnostics, tests, or prose while keeping the same policy_version.
2. A distribution minor or major release MUST NOT be assumed to change the
   policy contract unless its release notes explicitly say so.
3. A change to required fields, accepted core values, field meaning, report
   semantics, or migration guarantees MUST publish a new policy version,
   compatibility notes, and a migration and rollback path.
4. A validator MUST report the policy version it evaluated and MUST NOT
   silently treat a different policy version as equivalent.

The current v0.3 declaration therefore remains policy_version: "0.3" even
when it is distributed in a later v0.3.x release. A future v0.4 declaration
will use policy_version: "0.4" only after the v0.4 contract and migration
evidence are published.

## 4. Unknown fields and extension keys

Core policy objects are closed by default:

- Unknown top-level fields MUST be rejected.
- Unknown fields inside a known core object MUST be rejected.
- An implementation MUST NOT silently ignore a misspelled or unsupported
  core field.
- Repository-specific additions MUST be placed under the explicit top-level
  extensions object.
- Every extension key MUST begin with x-.

Extension values are implementation-defined and MUST NOT be interpreted as
UGS Core guarantees unless a future policy contract standardizes them. An
extension may be preserved during migration, but a consumer that cannot
interpret it MUST report that limitation when the extension affects the
claimed conformance result.

An extension key does not create a new Core field, alias, or conformance level.
Moving an extension into the Core namespace requires a documented policy
version change and a compatibility decision.

## 5. Compatibility classes and deprecation

Every policy field and accepted value belongs to one compatibility class:

- Stable: meaning and accepted type remain compatible within the policy
  version.
- Alias: a legacy spelling accepted for the canonical term; new output uses
  the canonical spelling.
- Deprecated: still accepted, but implementations emit a warning and
  migration guidance identifies its replacement.
- Extension: opt-in data under extensions.x-*; it has no Core meaning until
  standardized.

Deprecation is not complete until the documentation records the canonical
replacement, compatibility class, warning behavior, earliest retirement
release, and migration note. An alias or deprecated value MUST NOT be removed
from a policy version without an explicit compatibility decision and versioned
migration evidence.

For this contract, release and merge are retained v0.3 aliases. They are not
errors, but canonical v0.4 prose and tooling should use release-line and
merge-commit when referring to their semantics.

## 6. Negotiation, downgrade, and rollback

Compatibility is explicit. A consumer MUST identify the policy version it
supports before validating or rewriting a manifest. It MUST fail clearly when
the version is unsupported rather than guessing based on a distribution
SemVer or silently dropping fields.

A downgrade to an older policy version or validator is allowed only when:

1. the operator explicitly requests the downgrade;
2. the current manifest, policy version, and unsupported fields are inventoried;
3. a recoverable backup is written before any mutation;
4. the target version can represent the resulting declaration without
   changing the meaning of accepted evidence; and
5. a report records removed, translated, preserved, and rejected fields.

If a stronger guarantee cannot be represented by the target version, the
downgrade MUST fail or produce an explicitly weaker declaration that names the
weaker policy and conformance level. It MUST NOT silently claim the stronger
guarantee after downgrade. Rollback MUST restore the prior manifest and
preserve the original protected history.

## 7. Stable diagnostics and report contracts

UGS validators expose stable machine-readable diagnostics in addition to
human-readable messages. The shared Bash contract is implemented by
`scripts/ugs_errors.sh`: text mode emits `UGS-...: message` on stderr, while
`UGS_ERROR_FORMAT=json` emits one object with exactly these fields:

```json
{
  "format": "ugs-error/v1",
  "code": "UGS-POLICY-010",
  "message": "invalid conformance_level"
}
```

Error codes are stable identifiers within the policy contract. Consumers MUST
branch on `code`, not on the diagnostic prose. `UGS-0000` is reserved for a
successful result and MUST NOT identify a failure. A validator that cannot
provide a more specific check code MUST use its deterministic `UGS-CHECK-*`
fallback rather than inventing a free-form identifier.

`scripts/ugs_check.sh --format json` emits the `ugs-conformance/v0.3` report
shape until a future policy version explicitly changes it:

```json
{
  "format": "ugs-conformance/v0.3",
  "schema_version": 1,
  "result": "pass",
  "checks": [
    {
      "name": "policy manifest",
      "status": "pass",
      "code": "UGS-0000",
      "output": ""
    }
  ]
}
```

The top-level field set is `format`, `schema_version`, `result`, and `checks`.
Each check object contains `name`, `status`, `code`, and `output`;
`status` is `pass` or `fail`, and a passing check MUST use `UGS-0000`.
Implementations MUST preserve this shape when producing the v0.3 report and
MUST publish a versioned compatibility decision before adding or renaming
fields. The independent fixture runner also includes `code` alongside each
fixture's `id`, `status`, and `reason`, so Bash and Python implementations can
be compared without parsing prose.

## 8. Conformance expectations

An implementation claiming this contract MUST:

- accept the v0.3 aliases defined in the vocabulary table when validating a
  v0.3 manifest;
- render or report the canonical semantic terms without changing the wire
  values unless an explicit migration is requested;
- reject unknown Core fields and invalid extension keys;
- distinguish policy_version from distribution SemVer in diagnostics and
  release notes; and
- make deprecation, downgrade, and rollback behavior observable in its report.

The v0.4 contract is intentionally additive. Stable error identifiers and
normalized JSON reports are delivered by CR-0072. The executable offline
migration command is delivered by CR-0073: `migrate` inventories a repository,
supports a no-write dry run, emits the versioned `ugs-migration/v1` report,
writes a digest-and-mode backup, and verifies rollback; `upgrade` remains the
compatibility alias.
