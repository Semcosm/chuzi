# Repository Guidelines

## Current State

This repository is in the cross-platform foundation and domain-boundary stage for the chuzi service. The Go control-service entry point now loads deployment configuration, opens the single-node bbolt store, and assembles the request, queue, and session-runner boundaries into a persistent scheduler loop. The Node.js browser-worker protocol boundary, Rust/Wry browser-runtime helper, build manifests, contract tests, deterministic account state-machine package, encrypted credential boundary, and transport-neutral Matrix boundary are also present. The default service backend remains the Node deferred worker; Rust selection is explicit. Real account browser automation, a production Matrix network client, credential ingress/use, and deployment/operations integration remain planned work; the existing build/package scripts are not a complete production deployment workflow.

Read the chuzi project documents before adding implementation code:

- `README.md`: project scope and current stage.
- `docs/architecture.md`: component boundaries and planned layout.
- `docs/account-state-machine.md`: business-state source of truth.
- `docs/storage.md`: single-node storage, transaction, and recovery contract.
- `docs/security.md`, `docs/matrix-api.md`, and `docs/operations.md`: security, Matrix, and operational constraints.

The root `README.md` and `docs/` are chuzi documentation. `.ugs/docs/` contains copied UGS guidance and is not the project design documentation.

## Repository Layout

- `.ugs/`: UGS v0.3 policy, installation/bootstrap metadata, schemas, templates, and copied UGS reference docs.
- `.githooks/`: managed Git hooks. The current checkout contains only `commit-msg`; `core.hooksPath` is `.githooks`.
- `scripts/`: UGS initialization, package upgrade/activation/rollback wrappers, validators, and profile fixtures.
- `adapters/bare-git/`: Git server-side ref-update adapter.
- `adapters/github/`: GitHub compatibility and release helpers installed by the standard profile.
- `cr/`: UGS CR template and README; persisted change records use `cr/CR-*.md`.
- `keys/`: public signer-role and allowed/revoked-signer metadata only. Never add private keys.
- `.github/workflows/`: the checked-in UGS workflow.
- `browser-worker/`: Node.js Worker protocol boundary and build package.
- `cmd/` and `internal/`: Go control-service entry point, account domain, config, store, and shared protocol packages.
- `migrations/`: repeatable bbolt schema migrations and version metadata.

Do not treat the UGS files as application modules. Keep credentials, cookies, browser profiles, runtime data, logs, and generated artifacts out of Git; the existing `.gitignore` covers the repository's secret and runtime directories.

## Architecture Constraints

The planned service has these logical boundaries: Matrix adapter, request service, queue/scheduler, session runner, state store, credential store, and status notifier. Keep the following boundaries when implementation begins:

- The account state machine owns externally visible business state. Browser/session code reports runtime facts and must not decide business state directly.
- Each account uses an isolated browser Profile and lease. Do not combine profiles or accept arbitrary user input as a filesystem path.
- Credentials are encrypted at rest and exposed through least-privilege interfaces; logs, errors, screenshots, and Matrix messages must be redacted.
- Matrix code validates room/user authorization and exchanges requests or redacted domain events; it must not operate on credentials directly.
- Retry, timeout, lease-recovery, and duplicate-event behavior must be deterministic and represented in the state-machine design.
- Automate only accounts and services the user is authorized to use. Do not add credential theft, access-control bypass, CAPTCHA/risk-control evasion, or anti-detection behavior.

## Build, Test, and Local Checks

The initial application toolchains are Go 1.24 and Node.js 20+. The canonical cross-platform build commands are documented in `docs/operations.md` and are executed remotely by GitHub Actions.

The current repository-level checks are:

```bash
./scripts/validate_policy_manifest.sh
./scripts/validate_quality_profile.sh
./scripts/validate_supply_chain_profile.sh
./scripts/validate_action_pinning.sh
./scripts/validate_repository_shape.sh
./scripts/test_build_contract.sh
git diff --check
```

Application checks for the implemented Go boundaries are `go test ./...` and
`go vet ./...`; storage integration tests use temporary data directories and
never use production state.

When `.ugs/document-map.json` is present, also run
`./scripts/validate_document_map.py`. The standard GitHub workflow runs the
same profile checks and validates persisted CR coverage on main.

The policy validator requires `jq`. UGS upgrade/profile tooling uses `python3` and `git`; signature and attestation checks additionally use `ssh-keygen`, and GitHub adapters require the `gh` CLI.

Application build prerequisites are Go 1.24+, Node.js 20+, npm, and a compatible shell. The local machine does not need to build every target; GitHub Actions is the authoritative four-target build environment.

## GitHub CLI and Remote Workflow

The GitHub repository is `Semcosm/chuzi`. Use the configured `gh` credential
from the user's local GitHub CLI configuration (`~/.config/gh/hosts.yml`); do
not copy a PAT into the repository, shell history, command output, or a new
`.env` file. The repository's Git transport is SSH, and pushes use the
GitHub host alias and topic branch:

```bash
gh auth status
gh api user --jq .login
git push git@github-account:Semcosm/chuzi.git HEAD:<topic-branch>
```

If `gh auth status` reports an invalid token in a restricted or offline shell,
first retry from a network-enabled host environment and verify with
`gh api user --jq .login`; do not immediately overwrite the stored
credential. `gh auth login --with-token` is for an intentional credential
rotation only. The dedicated commit-signing key remains
`/home/chen/.ssh/chuzi-ugs-signing` and is unrelated to the GitHub API token.

Use explicit repository selectors when inspecting PRs or Actions:

```bash
gh pr view <number> --repo Semcosm/chuzi --json statusCheckRollup,mergeCommit,headRefOid,baseRefOid
gh run list --repo Semcosm/chuzi --branch <topic-branch>
gh run view <run-id> --repo Semcosm/chuzi --log-failed
gh run watch <run-id> --repo Semcosm/chuzi --exit-status
```

For an implementation CR, create the PR with the standard adapter so its body
is sourced from the persisted CR:

```bash
./adapters/github/create_pr_from_cr.sh cr/CR-XXXX-slug.md <topic-branch> Semcosm/chuzi
```

If a CR is revised after PR creation, update the PR body with
`gh pr edit <number> --repo Semcosm/chuzi --body-file cr/CR-XXXX-slug.md`.
The PR body must exactly match the persisted CR for UGS validation. A closure
PR may use a governance title while still using the CR file as its body.

Only merge after `ugs-validate` and the aggregate `chuzi-build` check pass.
The declared UGS strategy is `rebase-ff`; use the full, verified head SHA so
the merge cannot silently target a changed branch:

```bash
gh pr merge <number> --repo Semcosm/chuzi --rebase --match-head-commit <full-head-sha>
```

After merging, inspect `mergeCommit.oid` and the post-merge main checks. Use
that actual GitHub main commit when closing the CR's `Integrated Result`; do
not substitute the pre-rebase topic SHA.

For an existing consumer checkout, use an extracted official UGS release package for initialization. The checked-in `scripts/ugs_init.py` expects the release package's `bootstrap/templates/` directory, which is not part of this consumer checkout. Upgrades use the release archive explicitly and should be dry-run first:

```bash
./scripts/ugs.sh upgrade \
  --archive /path/to/ugs-bootstrap-vX.Y.Z.tar.gz \
  --dry-run /path/to/repository
./scripts/ugs.sh upgrade \
  --archive /path/to/ugs-bootstrap-vX.Y.Z.tar.gz \
  --backup-dir /path/to/ugs-backup \
  /path/to/repository
```

Use `./scripts/ugs.sh rollback --backup-dir <dir> <repository>` for a recorded upgrade rollback. Profile activation is explicit and must not be inferred from installed files.

## UGS Policy and Change Workflow

`.ugs/policy.json` is authoritative for this checkout: UGS v0.3, `standard` conformance, `continuous` branching, `rebase-ff` integration, protected `main`, signed daily commits, required commit trailers, and signed annotated semver release tags. The repository uses the official UGS v0.3.27 package and keeps `high-trust` components installed but inactive; do not activate `high-trust` without an explicit request.

Use the commit types declared in `.ugs/policy.json` and include at least one required trailer, normally `Refs:` and relevant `Tested-by:` evidence. Use short-lived topic branches from `main` with the declared prefixes `feat/`, `fix/`, `docs/`, `chore/`, or `test/`. For non-trivial changes, use `cr/CR-*.md` and `cr/TEMPLATE.md`; retain test evidence, risk, rollback, and breaking-change information.

The managed `commit-msg` hook checks the subject shape only; it does not replace policy or CR validation. There is currently no managed `pre-push` hook, so do not assume `git push` runs the repository checks automatically.

## Known Repository Traps

- `scripts/test_profile_conformance.sh` is a UGS bootstrap/package fixture. It expects the release package's `bootstrap/templates/` layout and is not a consumer-checkout test command.
- `scripts/ugs_check.sh` and the broad `test_*.sh` suite belong to the UGS source repository; this consumer checkout uses the standard profile workflow and its packaged validators.
- When copied UGS reference docs conflict with the machine-readable policy or `REPOSITORY_POLICY.md`, follow the current `.ugs/policy.json` declaration for this repository.

## Naming and Testing for Implementation

Use descriptive domain module names matching the architecture (`account`, `browser`, `credential`, `queue`, `matrix`, `store`, `config`, and `observability`). Use lowercase `snake_case` for database fields and configuration keys. Keep browser/session code behind interfaces so the domain state machine remains deterministic and testable. The current store is single-node bbolt; do not add multi-instance claims without a separate topology decision.

Every state transition and retry/timeout path needs unit coverage. The account
domain package is intentionally pure and uses caller-provided timestamps; keep
that property when extending it. Add integration coverage for persistence,
Matrix delivery, and browser-session lifecycle without using live accounts or
external credentials. Storage tests must use temporary directories and cover
migration repeatability, transaction atomicity, restart recovery, leases, and
backup behavior. Place module tests beside the implementation and cross-module
scenarios under `tests/` once those directories exist.
