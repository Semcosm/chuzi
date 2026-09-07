# UGS Document Map

The repository's top-level README is a human-facing index. Its `Document Map`
section is governed by the machine-readable
[`.ugs/document-map.json`](../../.ugs/document-map.json) tree.

## Model

The configuration uses the `ugs-document-map/v1` format:

- `document` identifies the Markdown document to check.
- `heading` identifies the `##` section containing the rendered map.
- `nodes` is a recursive ordered array. A node has a `title` and either a
  `path`, `children`, or both. A node without a path is a visual group.

Paths are repository-relative and must exist. Titles and paths are unique. The
validator compares the complete preorder traversal of the configured tree with
the Markdown list: titles, links, order, and two-space indentation must agree.
Links outside the configured heading are intentionally ignored, so the README
can contain ordinary navigation elsewhere.

## Change procedure

When adding or moving a document:

1. Update `.ugs/document-map.json` with the intended tree position.
2. Render the same node and nesting in `README.md`.
3. Run `scripts/generate_document_map.py` to render the section.
4. Run `scripts/validate_document_map.py` and `scripts/validate_repo.sh`.

The generator replaces only the configured `## Document Map` section and
preserves the rest of README. CI runs the validator in check mode, so a manual
edit to the generated section fails until the configuration is updated and the
section is regenerated.

The configuration is deliberately separate from `.ugs/policy.json`: policy
describes repository governance, while the document map describes navigation.
This keeps the recursive index extensible without adding document-specific
paths to the policy schema or validator.

Adoption is optional for UGS consumers. Bootstrap users can start with the
minimal template and opt in when their repository has enough documentation to
benefit from a maintained map.
