# Core API v1

`chuzi.core/v1` is the local, transport-neutral control-plane contract used by
the launcher façade. It exposes redacted Core DTOs only; native clients call the
launcher and must not open the Core endpoint, bbolt store, credentials, or Profile
directories, or depend on UI types.

## Wire Envelope

The transport is UTF-8 JSON Lines. Every request and response is one JSON
object terminated by `\n`.

Requests contain:

```json
{"protocol":"chuzi.core/v1","id":"c-1","method":"get_request","params":{"request_id":"req-1"}}
```

Responses contain either a result:

```json
{"protocol":"chuzi.core/v1","id":"c-1","type":"result","result":{"request_id":"req-1","state":"QUEUED"}}
```

or a stable error:

```json
{"protocol":"chuzi.core/v1","id":"c-1","type":"error","error":{"code":"not_found","message":"resource was not found"}}
```

`id` is required, must be unique while a call is in flight, and is used to
match concurrent responses. Method names are lowercase ASCII snake case.
Frames are limited to 1 MiB by default; implementations may configure a
limit from 1 KiB through 16 MiB. Oversized input or output is rejected without
returning the underlying payload.

## Handshake

The first request on a connection must be:

```json
{"protocol":"chuzi.core/v1","id":"hello-1","method":"hello","params":{"version":"chuzi.core/v1"}}
```

The result contains the negotiated version and supported method names:

```json
{"version":"chuzi.core/v1","methods":["hello","cancel","submit_request","get_request","get_account","cancel_request","get_result","list_events","list_notifications","get_browser_view","submit_diagnostic_report"]}
```

An unsupported protocol or version is reported as `unavailable`. Calls before
successful negotiation are rejected as `invalid_argument`.

## Methods

| Method | Parameters | Result |
| --- | --- | --- |
| `submit_request` | `coreapi.SubmitRequest` | `{request, idempotent}` |
| `get_request` | `{request_id}` | `coreapi.Request` |
| `get_account` | `{account_id}` | `coreapi.Account` |
| `cancel_request` | `coreapi.CancelRequest` | `coreapi.Request` |
| `get_result` | `{request_id}` | `coreapi.Result` |
| `list_events` | `coreapi.EventQuery` | `{events}` |
| `list_notifications` | `coreapi.NotificationQuery` | `{notifications}` |
| `get_browser_view` | `{request_id, width?, height?}` | `coreapi.BrowserView` |
| `submit_diagnostic_report` | `coreapi.DiagnosticReport` | `coreapi.DiagnosticStatus` |

`list_notifications` accepts optional `account_id`, `request_id`, `since`,
`until`, `offset`, and `limit` filters. Filtering, stable creation-time
ordering, and bounding are applied at the durable store boundary before the
redacted DTOs are projected.

`get_browser_view` is an ephemeral, read-only observation of an active request
session. Width and height default to 640x360 and are bounded to 160-1280 by
90-720. The result is a bounded `image/jpeg` frame encoded as base64. Core
accepts only the request ID; it never accepts a URL, CDP endpoint, Profile path,
mouse input, or keyboard input. If the request has no active browser session,
the method returns `unavailable`. Frames are not persisted, logged, audited, or
sent through Matrix.

`submit_diagnostic_report` is available only after an explicit user consent
action in the native client. The request contains a bounded severity, category,
and user-visible summary. Core adds only allow-listed platform and version
metadata plus a bounded window of already-redacted operational events. It never
reads credentials, browser Profiles, screenshots, page content, raw paths, or
unstructured log text. If the configured HTTPS endpoint is unavailable, the
service stores the bounded report in an owner-only local queue and retries with
backoff.

`cancel` is a transport operation, not a business-state command. Its
parameters are `{id}` and its result is `{cancelled}`. A client context
cancellation sends this operation for the in-flight call. `cancel_request`
remains the explicit account/request state-machine command.

The DTO definitions and JSON field names are in
[`internal/coreapi/api.go`](../internal/coreapi/api.go). Account, room, actor,
audit and resource identifiers returned by Core are stable redacted labels;
idempotency keys, room IDs, credentials, page content and filesystem paths are
never returned.

## Errors

The only public error codes are:

`invalid_argument`, `not_found`, `conflict`, `forbidden`, `unavailable`,
`cancelled`, `deadline_exceeded`, and `internal`.

Error messages are generic, stable descriptions selected by the code. Store,
bbolt, worker, adapter, credential and filesystem error text never crosses the
transport boundary.

## Local Endpoints and Permissions

The service derives its endpoint from the validated deployment `data_dir`; a
request cannot override it.

- Unix: `<data_dir>/core.sock`; the directory is `0700` and the socket is
  `0600`. A stale socket may be removed at startup, but an active endpoint is
  never replaced.
- Windows: `\\.\pipe\chuzi-core-<hash>`, where `<hash>` is the lowercase first
  eight bytes of SHA-256 over the cleaned absolute `data_dir` path encoded as
  UTF-8. The named pipe uses owner-only SDDL.

The OS endpoint permission is the local authorization boundary. The service is
not a network listener, and native clients must use the deployment-derived
endpoint rather than accepting arbitrary filesystem paths from users.

The contract suite includes a subprocess test that starts an independent Core
server and connects through the derived endpoint. It verifies version rejection,
redacted wire DTOs, endpoint permissions, and transport cancellation across the
process boundary; the Windows build runs the same test against the named-pipe
implementation.

## Compatibility

Clients must complete `hello` and may use the returned method list for feature
detection. Changes that alter existing field meaning, error semantics or
method behavior require a new protocol version; additive methods are exposed
through the method list and must not make existing v1 calls depend on UI state.
