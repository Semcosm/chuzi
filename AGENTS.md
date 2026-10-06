# Repository Guidelines

## Current State

This repository is in the cross-platform foundation and domain-boundary stage for the chuzi service. The Go control-service entry point now loads deployment configuration, opens the single-node bbolt store, and assembles the request, queue, and session-runner boundaries into a persistent scheduler loop. The Node.js browser-worker protocol boundary, build manifests, contract tests, deterministic account state-machine package, encrypted credential boundary, transport-neutral Matrix boundary, and Core API/native-client boundary are also present. The service backends are the Node deferred worker and explicit headless-CDP worker. Real account browser automation beyond the authorized Genshin Cloud Game check, a production Matrix deployment, and broader operations integration remain planned work; the existing build/package scripts are not a complete production deployment workflow.

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

### Linux + Codex UI Tooling

Windows UI work is performed from an Arch Linux terminal with Codex. Do not
make VS Code or an editor extension a repository prerequisite.

Before changing the Windows client, read docs/ui/README.md and
docs/ui/tooling.md together with the relevant UI contract documents. The
workstation must have ~/.cargo/bin on PATH and should expose:

- rust-analyzer for Rust and generated Slint bindings;
- slint-lsp for .slint diagnostics;
- cargo-nextest for the fast Rust test path;
- just for optional local command aliases.

The current Slint runtime is pinned in ui/windows/Cargo.toml. Keep slint-lsp
within the same 1.18.x line when changing that dependency.

The optional Slint MCP feature is for local development inspection only:

~~~bash
cargo check --manifest-path ui/windows/Cargo.toml --features slint/mcp --locked
SLINT_EMIT_DEBUG_INFO=1 SLINT_MCP_PORT=8080 \
  cargo run --manifest-path ui/windows/Cargo.toml \
  --features slint/mcp --locked
~~~

Never enable that feature in release packaging, expose its port outside the
developer host, or use it to inspect credentials, Profile paths, raw browser
data, or production session contents. Playwright is reserved for browser-worker
or web-surface checks; native Slint verification uses the Slint test backend,
deterministic layout snapshots, and Windows UI Automation in the Windows CI
environment. The complete workflow lives in docs/ui/tooling.md.

The layout snapshot example is a required UI check for state, theme, and size
coverage. It currently validates dimensions and non-blank output; do not claim
pixel-level golden comparison until that tool is added and documented.

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

Use explicit repository selectors for one-off inspection:

```bash
gh pr view <number> --repo Semcosm/chuzi --json statusCheckRollup,mergeCommit,headRefOid,baseRefOid
```

### Silent GitHub Actions waiting

After a push, do not make the agent a CI poller. Do not repeatedly call
`gh run list`, `gh run view`, or the GitHub API; do not read queued, pending,
in-progress, runner, or step-progress state into the model context. Do not
implement an Actions polling loop, call the API every few seconds, or stream
`gh run watch` progress into the model context. Use the repository helper for
one bounded, silent wait:

```bash
./scripts/ci-wait "$(git rev-parse HEAD)"
```

The helper finds the `chuzi-build` run for the exact commit internally, waits
with a 30-second refresh interval, and has a 30-minute hard timeout. It emits
only one final line: `CI SUCCESS: <commit>`, `CI FAILED: <commit> (run <id>)`,
or `CI TIMEOUT: <commit> (exceeded 30m)`. A timeout is not a CI failure. Do not
load complete successful workflow logs. After `CI FAILED`, use the reported
run id only if diagnosis is needed, and bound the failed-step output:

```bash
gh run view <run-id> --repo Semcosm/chuzi --log-failed 2>&1 | sed -n '1,200p'
```

Set `CI_WORKFLOW` to override the default workflow or pass a second duration
such as `./scripts/ci-wait "$commit" 45m`. If the helper reports `CI TIMEOUT`,
stop waiting and report the timeout without assuming the code failed.

### Build Nightly From an Unmerged Topic Branch

To build all four nightly targets before merging a topic branch, push the
committed branch over the configured SSH alias and dispatch `chuzi-build` on
that branch. Do not rely on a local `scripts/build.sh` run for this purpose:
the local host can build only its native target, while the Actions matrix
builds `windows-amd64`, `linux-amd64`, `linux-arm64`, and `darwin-arm64`.

```bash
topic_branch="$(git branch --show-current)"
head_sha="$(git rev-parse HEAD)"
git status --short
git push git@github-account:Semcosm/chuzi.git HEAD:"$topic_branch"

gh auth status
gh api user --jq .login
gh workflow run chuzi-build.yml --repo Semcosm/chuzi --ref "$topic_branch"
./scripts/ci-wait "$head_sha"
```

After the helper returns `CI SUCCESS`, perform the following single lookup for
the completed run whose `headSha` exactly equals `head_sha`; do not use it as a
polling loop. Then derive the workflow version and validate every downloaded
Actions artifact against the topic commit:

```bash
run_id="$(gh run list --repo Semcosm/chuzi --workflow chuzi-build.yml \
  --commit "$head_sha" --status completed --limit 20 \
  --json databaseId,headSha,conclusion,createdAt \
  --jq 'map(select(.headSha == "'"$head_sha"'" and .conclusion == "success")) | sort_by(.createdAt) | last.databaseId')"
test -n "$run_id"
run_number="$(gh run view "$run_id" --repo Semcosm/chuzi --json number --jq .number)"
nightly_version="nightly-${run_number}-${head_sha:0:12}"
download_root="$(mktemp -d)"
trap 'rm -rf "$download_root"' EXIT
for target in windows-amd64 linux-amd64 linux-arm64 darwin-arm64; do
  target_dir="$download_root/$target"
  mkdir -p "$target_dir"
  gh run download "$run_id" --repo Semcosm/chuzi \
    --name "chuzi-nightly-$target" --dir "$target_dir"
  python3 scripts/validate_release_index.py \
    --index "$target_dir/chuzi-${nightly_version}-${target}.index.json" \
    --dist "$target_dir"
  ./scripts/validate_nightly_artifact.py \
    --artifact "$target_dir" --target "$target" \
    --commit "$head_sha" --version "$nightly_version"
done
```

`scripts/accept_nightly_run.sh` is the main-branch acceptance flow and
currently rejects topic branches by design; use the per-target validation
commands above for an unmerged branch. If an HTTPS push is rejected because
the OAuth token lacks the `workflow` scope, do not rotate credentials just for
this operation; use the configured SSH push command shown above.

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
