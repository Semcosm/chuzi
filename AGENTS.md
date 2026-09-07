# Repository Guidelines

## Project Structure & Module Organization

This repository is currently in the architecture-planning phase and is governed by UGS v0.3.27. Project design lives in `docs/`; read `docs/architecture.md` before adding implementation code. The planned application layout is:

- `cmd/`: executable entry points.
- `internal/`: account state machine, browser sessions, credentials, queueing, Matrix integration, storage, configuration, and observability.
- `migrations/`, `tests/`, `configs/`, and `deploy/`: persistence, integration tests, example configuration, and deployment assets.
- `cr/`: UGS change-request records; `.ugs/`, `.githooks/`, and `scripts/`: repository governance.

Keep credentials, browser profiles, runtime data, and generated artifacts out of Git.

## Build, Test, and Development Commands

No application build system or test runner has been selected yet. Until implementation begins, run:

```bash
./scripts/validate_policy_manifest.sh
git diff --check
```

The first command validates UGS policy metadata; the second catches whitespace errors. Once code exists, document canonical build, test, lint, and local-run commands here and in `docs/operations.md`.

## Coding Style & Naming Conventions

Follow the formatter and linter selected for the implementation language; do not introduce competing style rules without documenting the decision. Use lowercase `snake_case` for database fields and configuration keys, `kebab-case` for topic branches, and descriptive nouns for domain modules (`account`, `queue`, `credential`, `matrix`). Keep browser/session code behind interfaces so the domain state machine remains deterministic and testable.

## Testing Guidelines

Every state transition and retry/timeout path requires unit tests. Add integration tests for persistence, Matrix delivery, and browser-session lifecycle; keep external credentials and live accounts out of automated tests. Place tests beside the module or under `tests/` for cross-module scenarios. Record commands and results in change requests.

## Commit & Pull Request Guidelines

Use conventional commit types declared by UGS, for example `feat: add account queue` or `docs: clarify Matrix events`. Branches use prefixes such as `feat/`, `fix/`, `docs/`, `test/`, and `chore/`. Commits require the repository’s configured trailers. Changes should include a matching `cr/CR-XXXX-*.md` record when applicable, test evidence, risk and rollback notes, and a focused pull request description. Sensitive-path changes require maintainer acknowledgement.

## Security & Configuration

Never commit passwords, tokens, cookies, Matrix access tokens, encryption keys, or unredacted screenshots/logs. Store credentials encrypted and inject secrets through the deployment environment. Each account must use an isolated browser Profile and lease; do not implement credential theft, access-control bypass, or anti-detection evasion.
