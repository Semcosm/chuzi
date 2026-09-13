# Production deployment

The service is a single-node process. Keep `data_dir`, the bbolt database,
browser Profiles, and backups on one persistent filesystem owned by the service
user. Do not run two service instances against the same data directory.

1. Copy `configs/example.json` and set an absolute or deployment-relative
   `data_dir`. Configure the Matrix homeserver, bot user, token environment
   variable, and explicit room/user policy only when Matrix is enabled.
2. Inject `CHUZI_CREDENTIAL_KEY_ID`, `CHUZI_CREDENTIAL_KEY`, and the configured
   Matrix token through the deployment secret manager. They must not be put in
   JSON config, Git, logs, or command arguments.
3. Start `cmd/service` with the config path and the externally installed
   browser command required by the selected backend. The optional health
   listener should bind to loopback or a protected management network.
4. Create backups with `chuzi -backup`. Restore only a validated file emitted
   under `<data_dir>/backups/` while the service is stopped:
   `chuzi -restore <backup-path>`.

The HTTP Matrix client uses the Client-Server `sync`, `send`, and `whoami`
endpoints over the configured origin. Access tokens stay in process memory and
are never returned in errors. Failed notification sends remain in the durable
outbox and are retried with the stable event ID.

This directory does not define a container image, secret manager integration,
or stable release signing workflow. Signed annotated semver releases remain a
separate follow-up change after production runtime evidence is collected.
