# UGS Conformance Levels And Profile Matrix

## 1. Conformance levels

A v0.3 repository **MUST** declare one `conformance_level` in
`.ugs/policy.json`.

| Level | Minimum declaration and evidence |
| --- | --- |
| `baseline` | Valid policy manifest, commit convention, CR record, and local conformance checks. |
| `standard` | `baseline`, protected refs, required CI checks, review evidence, and signed release tags. |
| `high-trust` | `standard`, trusted signed commits, signed fast-forward integration, and authoritative protected-ref enforcement. |

Levels add requirements; they do not change the meaning of `continuous` or
`release-line`. A repository **MUST NOT** claim a level without satisfying all
requirements for that level.

## 2. Profile and merge-strategy matrix

The branch profile describes branch topology. The merge strategy describes the
single normal integration operation. A repository **MUST** declare both.

| Branch profile | `rebase-ff` | `merge` | `squash` |
| --- | --- | --- | --- |
| `continuous` | Recommended; preserves linear topic history. | Allowed when the merge commit is signed and recorded. | Allowed when the source range and resulting commit are recorded. |
| `release-line` | Allowed for a linear maintenance line. | Recommended; preserves release-line integration boundaries. | Allowed when the source range and resulting commit are recorded. |

The matrix permits a strategy but does not relax signing, review, CR, or
protected-ref requirements imposed by the declared conformance level.

The selected strategy **MUST** be recorded in each new v0.3 CR so the source
range and final integration object can be interpreted deterministically.

When trailer review evidence is claimed, the final integration object must
carry both the review and test conclusions; source-only trailers are
insufficient after rewrite or squash.

## 3. Current repository declaration

UGS declares `continuous`, `rebase-ff`, and `high-trust`. This combination is
the reference high-trust path: topic branches are rebased and integrated by a
signed fast-forward update to protected `main`.

## 4. Compatibility

Conformance levels are an additive v0.3 profile. They do not retroactively
invalidate accepted v0.2 history. A future profile may add stricter evidence
without changing the two branch-profile definitions.
