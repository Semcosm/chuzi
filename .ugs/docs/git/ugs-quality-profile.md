# UGS Quality Profile

The quality profile is an optional v0.3 declaration for repository-level
quality expectations. It supplements the Core conformance contract and does
not change Git governance, accepted history, or v0.2 compatibility boundaries.

A repository opts in by adding `quality` to `.ugs/policy.json`:

```json
{
  "quality": {
    "profile": "basic",
    "required_documents": ["README.md", "CONTRIBUTING.md", "RELEASE.md"],
    "test_entrypoints": ["scripts/validate_repo.sh"]
  }
}
```

`basic` requires the declared documents to exist and each declared test entry
point to be executable. `standard` has the same rules and additionally MUST
declare `LICENSE`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, and `SUPPORT.md`.

The profile is additive: repositories without `quality` remain valid v0.3
repositories. A published profile must not silently weaken its requirements.
