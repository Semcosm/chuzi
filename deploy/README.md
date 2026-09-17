# Production deployment

The service is a single-node process. Keep `data_dir`, the bbolt database,
browser Profiles, and backups on one persistent filesystem owned by the service
user. Do not run two service instances against the same data directory.

1. Copy `configs/example.json` and set an absolute or deployment-relative
   `data_dir`. Configure the Matrix homeserver, bot user, token environment
   variable, and explicit room/user policy only when Matrix is enabled.
2. Inject `CHUZI_CREDENTIAL_KEY_ID`, `CHUZI_CREDENTIAL_KEY`, and the configured
   Matrix token through the deployment secret manager. They must not be put in
   JSON config, Git, logs, or command arguments. During a key rotation window,
   `CHUZI_CREDENTIAL_KEYS` may contain a bounded JSON map of historical key IDs
   to encoded key material; remove old entries after `-rotate-account` has
   covered every active credential.
3. Start `cmd/service` with the config path and the externally installed
   browser command required by the selected backend. The optional health
   listener should bind to loopback or a protected management network.
4. Create backups with `chuzi -backup`. Restore only a validated file emitted
   under `<data_dir>/backups/` while the service is stopped:
   `chuzi -restore <backup-path>`.
5. Before and after a recovery drill, run `chuzi -validate-backup <path>` and
   `chuzi -diagnostics`. The latter reports schema, queue, lease, outbox, and
   database-size counts without exposing account or room identifiers. Use
   `chuzi -audit` for the bounded, metadata-only audit view.
6. Provision credentials with `chuzi -inject-account <account> -credential-env
   <secret-env>`, rotate with `chuzi -rotate-account <account>`, and revoke
   with `chuzi -revoke-account <account>`. These commands print only redacted
   metadata and run as stopped-service maintenance operations.

The HTTP Matrix client uses the Client-Server `sync`, `send`, and `whoami`
endpoints over the configured origin. Access tokens stay in process memory and
are never returned in errors. Failed notification sends remain in the durable
outbox and are retried with the stable event ID.

## Runtime verification

Run `scripts/test_runtime.sh` before a deployment change. It uses fake accounts,
temporary bbolt data, test credentials, and a protocol-shaped local Matrix
homeserver. In a controlled environment, set all `CHUZI_MATRIX_TEST_*` values
to disposable test users/room and additionally set
`CHUZI_RUN_CONTROLLED_MATRIX=1`; the opt-in test then verifies `whoami`, sync,
bot send, and the Gateway/Notifier path against the real test homeserver. Never
point these variables at a production room or token.

## Observability

The optional `observability` block in `config.json` enables a local
Prometheus-compatible endpoint (`metrics_listen`) and owner-only structured
JSONL logs (`log_path`). `log_max_bytes` and `log_max_files` bound rotation;
empty values keep metrics disabled and write redacted logs to stderr. Metrics
labels contain only fixed low-cardinality component/operation/outcome classes.
Never expose either endpoint directly to an untrusted network; put them behind
the deployment's local firewall or management proxy.

The health endpoint runs dependency probes concurrently. Its probe timeout is a
runtime safety bound, distinct from test deadlines; a caller cancellation is
propagated and does not turn a cancelled request into business failure.

This directory does not define a container image, secret manager integration,
or stable release signing workflow. Signed annotated semver releases remain a
separate follow-up change after production runtime evidence is collected.
