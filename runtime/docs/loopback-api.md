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
