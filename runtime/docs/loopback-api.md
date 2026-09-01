# Authenticated loopback API

The daemon binds an OS-assigned TCP port on `127.0.0.1`. The bind address is not
configurable in the MVP, so startup cannot accidentally expose the API on a LAN
or wildcard interface.

After binding, each daemon start generates a cryptographically random 256-bit
bearer token and atomically publishes `%LOCALAPPDATA%\DARKSTAR\runtime\endpoint.json`.
The file is created with restrictive permissions and conforms to
[`endpoint-v1alpha1.schema.json`](../../schemas/endpoint-v1alpha1.schema.json):

```json
{
  "schemaVersion": 1,
  "apiVersion": "v1",
  "pid": 1234,
  "processStartedAt": "2026-08-31T20:00:00.1234Z",
  "port": 49152,
  "token": "<64 lowercase hexadecimal characters>",
  "createdAt": "2026-08-31T20:00:01Z"
}
```

The discovery snapshot is published before `daemon.json`, so a process reported
as running already has an accepting API. Normal shutdown removes `endpoint.json`
only when it is still owned by that server instance. Token rotation atomically
replaces the entire snapshot; an old token fails immediately and tokens are never
placed in command lines, URLs, log output, events, or ordinary formatted values.

## Versions and authentication

`GET /api/v1/health` is the only unauthenticated operation. It reports process health
and the supported API versions without exposing endpoint credentials. Every
other request requires the exact `Authorization: Bearer <token>` value from the
discovery file.

Clients read `apiVersion`, intersect it with their supported versions, and use
the corresponding `/api/v1` base. An authenticated request for another `/api/*`
version receives HTTP 426 with stable code `API_VERSION_UNSUPPORTED`. The API
root at `GET /api/v1/` confirms the negotiated representation version.
Both the API root and health response include the safe startup-recovery counts:
how many durable records were classified and how many require operator
reconciliation. Scheduling admission is derived from the unresolved count.
Authority evidence remains authenticated durable state and is not returned by
the health endpoint.

`GET /api/v1/doctor` is authenticated and returns the detailed database, daemon,
path, Git, Codex, GitHub, configuration, and provider report. Each check has one
closed readiness state and stable uppercase code. Degraded and unhealthy checks
also carry a safe remediation action; command output and credentials are never
included. The report status is derived from the worst check, and independent
checks run concurrently so a slow external CLI probe does not serialize the
entire diagnostic. The optional absolute `projectRoot` query selects the Git and
project-configuration context; the CLI always supplies its current directory so
a persistent per-user daemon does not report a repository left over from startup.

`GET /api/v1/events` is an authenticated Server-Sent Events feed over the
authoritative global event sequence. Each message ID is its decimal global
position, `Last-Event-ID` resumes strictly after that position, and live commits
arrive on the same connection. Clients deduplicate by message ID across reconnects;
keepalives are comments and do not advance the cursor. A position outside retained
history fails explicitly instead of silently restarting from the beginning.

`GET /api/v1/logs/{reference}` reads append-only logs through opaque references.
The `after` byte cursor and bounded `limit` query return raw bytes plus
`X-Darkstar-Log-Next-Offset`, `X-Darkstar-Log-Size`, and
`X-Darkstar-Log-Complete`. Following clients request successive offsets; the API
never accepts a filesystem path or returns an unbounded log body.

`POST /api/v1/runs` accepts the closed `fake-success` and `fake-restart`
scenarios plus a required `Idempotency-Key`. It returns HTTP 202 only after the
run and attempt creation events and command response evidence are durable;
provider execution then belongs to the daemon lifetime. `GET
/api/v1/runs/{runId}` returns the rebuildable run projection and its attempt
projections. On startup, an active fake attempt is reconstructed from its
scenario, provider identity, and last sequence and resumes strictly after the
last durable event, so provider events and terminal transitions are not
duplicated.

`GET /api/v1/runs/{runId}/export` returns a finite `application/zip` support
bundle. It contains `run.json`, correlated `events.jsonl`, `commands.json`, an
`artifacts/index.json`, and every discovered log that remains locally available.
`manifest.json` identifies the `default-v1` redaction policy, records each
payload entry's SHA-256 digest and size, and lists unavailable or withheld
evidence as explicit omissions. JSON fields with credential semantics, opaque
locators, bearer values, and common credential assignments are redacted again at
the export boundary; logs receive the same value redaction. The manifest itself
is not listed in its entries because it cannot contain its own digest.

The CLI client performs this discovery and negotiation once per finite command.
For missing or unreachable endpoint state it idempotently autostarts the daemon,
then performs one fresh discovery attempt. It never replays an application
mutation automatically.

## Stable errors

All HTTP failures use the versioned API error envelope: `schemaVersion`, stable
uppercase `code`, safe `message`, `requestId`, `retryable`, and optional field
details. The same request ID is returned in `X-Request-Id`. Authentication is
checked before route or version details for every non-health request.

Host, Origin, CSRF, and browser-specific same-origin hardening remains assigned
to DS-191; this foundation supplies the loopback bind, bearer authentication,
rotation, version negotiation, and stable error boundary it builds upon.
