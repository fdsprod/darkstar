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
included. Codex and provider checks may include the pinned executable, exact
version, credential-free authentication and usage readiness, effective
instruction-source categories, conflicting executable identities, and separate
available/unavailable capability lists. Account identifiers, tokens, balances,
and raw provider responses are never part of this projection. The report status
is derived from the worst check, and independent
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

`POST /api/v1/runs` accepts exactly one of two request shapes plus a required
`Idempotency-Key`. A work-backed request names `workItemId`, `workflowId`, and
`workflowVersion`; the daemon validates the active project/work hierarchy,
freezes the installed workflow's default route, marks open work active, and
returns the queued run with HTTP 201. `GET /api/v1/runs` returns deterministic
bounded pages using `limit` and an opaque `after` run cursor.

The compatibility request accepts the closed `fake-success` and `fake-restart`
scenarios. It returns HTTP 202 only after the
run and attempt creation events and command response evidence are durable;
provider execution then belongs to the daemon lifetime. `GET
/api/v1/runs/{runId}` returns the rebuildable run projection and its attempt
projections. On startup, an active fake attempt is reconstructed from its
scenario, provider identity, and last sequence and resumes strictly after the
last durable event, so provider events and terminal transitions are not
duplicated.

Run controls are `POST /api/v1/runs/{runId}/{pause|resume|retry|continue|cancel}`.
Every control requires `Idempotency-Key` and a quoted positive `If-Match`
resource version. Pause, resume, and cancel have no body; retry accepts an
optional `nodeId`; continue requires an `until` terminal boundary. Successful
commands return the updated run and `ETag`. A stale version returns
`RUN_VERSION_CONFLICT`, an illegal lifecycle edge returns
`RUN_CONTROL_INVALID_TRANSITION`, and exact command retries replay their durable
response without applying the transition twice.

`GET /api/v1/runs/{runId}/export` returns a finite `application/zip` support
bundle. It contains `run.json`, correlated `events.jsonl`, `commands.json`, an
`artifacts/index.json`, and every discovered log that remains locally available.
`manifest.json` identifies the `default-v1` redaction policy, records each
payload entry's SHA-256 digest and size, and lists unavailable or withheld
evidence as explicit omissions. JSON fields with credential semantics, opaque
locators, bearer values, and common credential assignments are redacted again at
the export boundary; logs receive the same value redaction. The manifest itself
is not listed in its entries because it cannot contain its own digest.

## Project and work operations

Project and work mutations require `Idempotency-Key`, reject unknown request
fields, and return canonical opaque IDs. Repository locations and external work
references are command inputs; current projections expose only their SHA-256
source fingerprints.

| Method and route | Operation |
|---|---|
| `GET /api/v1/projects` | List registered project projections. |
| `POST /api/v1/projects` | Register one named project source. |
| `GET /api/v1/projects/{projectId}` | Show a project and its directly owned work items. |
| `GET /api/v1/work-items?projectId=...` | List all work or filter by one exact project. |
| `POST /api/v1/work-items` | Create authored work from a title and priority. |
| `POST /api/v1/work-items/import` | Import an external source reference with an optional local title. |
| `GET /api/v1/work-items/{workItemId}` | Show work with its run and story projections. |

The create and import request shapes are distinct so a request cannot be both
authored and externally sourced. Project and work lifecycle status remains the
single durable state; list and show responses do not store a second derived
active or terminal flag.

## Artifact operations

The authenticated artifact surface is a transport over the daemon's shared
artifact operation service. Mutation endpoints require `Idempotency-Key` and
accept or return JSON:

| Method and route | Operation |
|---|---|
| `POST /api/v1/artifacts` | Ingest one file, paste, or stdin payload as an immutable artifact version. |
| `GET /api/v1/artifacts` | List latest versions, optionally filtering by exact `targetKind` and `targetId`. |
| `GET /api/v1/artifacts/{artifactId}?version=N` | Show an exact version; omit `version` only to request latest. |
| `POST /api/v1/artifacts/{artifactId}/revisions` | Add a new immutable version to an existing artifact identity. |
| `GET /api/v1/artifacts/{artifactId}/diff?from=N&to=M` | Compare content, media type, sensitivity, roles, tags, and representation kinds. |
| `POST /api/v1/artifacts/{artifactId}/extract?version=N` | Derive safe representations with the configured processor set. |
| `GET /api/v1/artifacts/{artifactId}/lint?version=N` | Report storage, freshness, representation, and diagnostic findings. |
| `GET /api/v1/artifacts/{artifactId}/representations?version=N` | Inspect all representations for an exact artifact version. |
| `POST /api/v1/artifacts/{artifactId}/impact?version=N` | Assess late-evidence coverage and return closed action proposals. |
| `POST /api/v1/artifact-bindings` | Attach an exact artifact version to one supported target. |
| `DELETE /api/v1/artifact-bindings/{bindingId}` | Append an unbound state without deleting binding history. |

Request bodies reject unknown fields and multiple JSON values. Artifact content
is base64 in JSON and bounded to 36 MiB at the transport boundary. Exact version
queries are positive integers. Attachments name one closed target kind:
`project`, `work`, `run`, `node`, `checkpoint`, `decision`, `story`, or
`implementation_point`. Validation, missing resources, and attachment or
version conflicts use the ordinary stable API error envelope.

Artifact review decisions use
`POST /api/v1/approvals/{approvalId}/decisions` with both `Idempotency-Key` and
a quoted `If-Match` resource version. The body binds `approve`,
`request_changes`, or `reject` to the exact candidate `scopeDigest` and frozen
`policyDigest`, with an optional bounded feedback comment. An exact retry
returns the original round; a changed payload under the same key or a new
decision against an already-resolved round returns a stable approval conflict.
Each requested revision receives a new approval ID while retaining the stable
checkpoint ID, prior draft, feedback, and revision-driven descendant effects.

The CLI client performs this discovery and negotiation once per finite command.
For missing or unreachable endpoint state it idempotently autostarts the daemon,
then performs one fresh discovery attempt. It never replays an application
mutation automatically.

## Workflow operations

The authenticated workflow surface delegates to the daemon's workflow catalog:

| Method and route | Operation |
|---|---|
| `GET /api/v1/workflows` | List installed immutable versions, optionally filtered by `name`. |
| `POST /api/v1/workflows/validate` | Validate one normalized authored definition without installing it. |
| `POST /api/v1/workflows/install` | Canonicalize and immutably install one definition. |
| `GET /api/v1/workflows/show?name=...&version=...` | Return the typed definition and installation metadata; omitted version selects latest. |
| `GET /api/v1/workflows/graph?name=...&version=...` | Return a stable authored node/edge projection. |
| `POST /api/v1/workflows/preview?name=...&version=...` | Validate boundaries and derive the candidate frozen route. |

List responses omit canonical document bytes; show is the single finite document
representation. Validation reports carry one authoritative issue list rather than
an independently stored validity flag. Invalid route boundaries return the normal
422 `VALIDATION_FAILED` envelope with ordered workflow issue details.

## Stable errors

All HTTP failures use the versioned API error envelope: `schemaVersion`, stable
uppercase `code`, safe `message`, `requestId`, `retryable`, and optional field
details. The same request ID is returned in `X-Request-Id`. Authentication is
checked before route or version details for every non-health request.

Host, Origin, CSRF, and browser-specific same-origin hardening remains assigned
to DS-191; this foundation supplies the loopback bind, bearer authentication,
rotation, version negotiation, and stable error boundary it builds upon.
