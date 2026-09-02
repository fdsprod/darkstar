# CLI API client and machine output

The CLI is a transport client for the daemon. Commands that need application
state discover `%LOCALAPPDATA%\DARKSTAR\runtime\endpoint.json`, negotiate the
published API version, send the bearer token only in the `Authorization` header,
and call the versioned loopback API. They do not reimplement server validation,
state transitions, authorization, or other business rules.

`darkstar api status` exercises this boundary. It discovers a running daemon or
idempotently autostarts one, authenticates `GET /api/v1/`, validates the response
version, and reports readiness. Lifecycle inspection is intentionally different:
`darkstar daemon status` is read-only and never autostarts the daemon.

`darkstar doctor` uses the same discovery and authentication path, then requests
`GET /api/v1/doctor`. Human output lists every subsystem state, stable diagnostic
code, and required action. Provider sections additionally list the pinned
executable, exact version, authentication and usage readiness, instruction
source categories, executable conflicts, and final capability states. `--json`
returns the versioned report unchanged. A
degraded or unhealthy report uses exit class 7 because the diagnostic completed
successfully with findings; transport failures retain their ordinary exit class.

`darkstar run start <work-id> --workflow <name> --version <version>` validates
the work item, resolves an exact installed workflow, freezes its default route,
and queues the run through `POST /api/v1/runs`. Omitting the workflow flags uses
the shipped `darkstar/mvp-walking-skeleton` `1.0.0` identity.
`darkstar run list` returns a bounded cursor page ordered by priority and
creation time.

`darkstar run start --scenario fake-success` preserves the M1 deterministic
fake-provider acceptance path through the same endpoint. `fake-restart` is the recovery
acceptance scenario: it persists its first provider event, waits for a daemon
restart, and resumes from the durable sequence cursor. `--idempotency-key <key>`
is optional for interactive use and lets automation safely repeat the same start.
`darkstar run show <run-id>` reads the persisted run and attempt projections;
`darkstar run watch <run-id>` replays the authenticated event stream until the
run reaches a terminal event, then prints the final persisted projection.

Run controls have direct CLI/API parity:

```text
darkstar run pause <run-id>
darkstar run resume <run-id>
darkstar run retry <run-id> [--node <id>]
darkstar run continue <run-id> --until <node>
darkstar run cancel <run-id>
```

Each command reads the current resource version and submits it as an optimistic
concurrency precondition. Pause moves queued or running work to `waiting` while
preserving the active attempt cursor; resume requeues waiting or blocked work;
retry creates a fresh attempt without rewriting the failed attempt; continue
strictly extends a completed frozen route; and cancel closes active children and
terminates a live provider handle. Every control accepts `--idempotency-key` for
safe automation retries, and stale or illegal transitions fail explicitly.

`darkstar run export <run-id> --output <file>` downloads the daemon-created ZIP
without duplicating export or redaction logic in the client. The CLI resolves the
output to an absolute path, writes a protected temporary file in the destination
directory, and atomically publishes it. It refuses to overwrite an existing
file. With `--json`, stdout contains the run ID, absolute output path, and byte
size while the bundle remains at the requested path.

## Project and work commands

Project and work commands use the same authenticated daemon boundary as other
stateful commands:

```text
darkstar project add [path] [--name <name>]
darkstar project register [path] [--name <name>]
darkstar project list
darkstar project show <project-id>
darkstar work create <description> [--project <project-id>] [--priority <n>]
darkstar work import <source-ref> [--project <project-id>] [--title <title>] [--priority <n>]
darkstar work list [--project <project-id>]
darkstar work show <work-id>
```

`project add` and `project register` are equivalent. The CLI canonicalizes an
existing directory, sends it as registration source, and the daemon persists
only its SHA-256 fingerprint. Re-registering the same source returns the same
project instead of creating a second repository identity.

Authored work fingerprints its description. Imported work fingerprints the
external source reference and defaults its local title to that reference. When
`--project` is omitted, exactly one active registered project must exist; an
ambiguous selection fails explicitly. Successful JSON output uses the common
`{"schemaVersion":1,"result":...}` envelope, while project and work show
results include their directly owned aggregate projections.

## Artifact commands

Artifact commands use the same daemon-owned application service as the HTTP API:

```text
darkstar artifact ingest (--file <path> | --paste <text> | --stdin) [--media-type <type>] [--role <role>]... [--tag <tag>]... [--sensitivity <level>]
darkstar artifact revise <artifact-id> (--file <path> | --paste <text> | --stdin) [metadata options]
darkstar artifact attach <artifact-id>@<version> --to <kind>:<id>
darkstar artifact detach <binding-id>
darkstar artifact list [--target <kind>:<id>]
darkstar artifact show <artifact-id>@<version>
darkstar artifact diff <artifact-id> --from <version> --to <version>
darkstar artifact extract <artifact-id>@<version>
darkstar artifact lint <artifact-id>@<version>
darkstar artifact representations <artifact-id>@<version>
darkstar artifact impact <artifact-id>@<version> --target <kind>:<id> [--run <run-id>]
```

Ingestion accepts exactly one source and reads at most 25 MiB. File media type is
inferred from its extension unless overridden. Sensitivity is one of `unknown`,
`public`, `internal`, `sensitive`, or `secret`; target kind is one of `project`,
`work`, `run`, `node`, `checkpoint`, `decision`, `story`, or
`implementation_point`. Mutations generate a fresh idempotency key, while all
version-sensitive reads require an exact `<artifact-id>@<version>` reference.

Human `artifact lint` exits with class 7 when findings exist. In machine mode,
every successful artifact command returns `{"schemaVersion":1,"result":...}`;
the `result` value is the same resource returned by the HTTP operation.

## Workflow commands

Workflow commands use daemon-owned loading, validation, immutable installation,
and route derivation rather than duplicating those rules in the CLI:

```text
darkstar workflow list
darkstar workflow show <name> [--version <version>]
darkstar workflow validate <file>
darkstar workflow install <file>
darkstar workflow graph <name> [--version <version>]
darkstar workflow preview <name> [--version <version>] [--from <node>] [--until <node>]... [--input <json-file>]
```

JSON and YAML definition files receive the same size and safety checks as configured
workflow discovery. Omitting `--version` selects the highest installed semantic
version. Validation returns every stable issue and exits with class 7 when findings
exist. Installation is immutable and idempotent for identical canonical bytes.

`workflow graph` projects all authored nodes and transitions. `workflow preview`
derives the exact route-shaped projection for the selected entry and terminal
boundary, separating executable and excluded nodes and listing unresolved run
inputs. Repeating `--until` selects multiple terminal boundaries; `--input` accepts
one JSON object whose keys are workflow run-input names.

## Machine output

Every finite human-readable command accepts `--json` as its final argument. A
successful command writes exactly one JSON value to stdout and includes
`"schemaVersion": 1`; artifact commands place their resource under `result`.
It writes no progress prose to either stream. A failed
machine-mode command writes exactly one error value to stdout and uses its process
exit class for branching. Stderr is reserved for failures that prevent JSON from
being encoded or written.

Long-running streaming commands use JSON Lines instead of a single document.
`darkstar daemon run --json`, for example, emits versioned `running` and `stopped`
events. Human mode continues to write results to stdout and diagnostics to stderr.

Machine failures conform to
[`cli-error-v1alpha1.schema.json`](../../schemas/cli-error-v1alpha1.schema.json):

```json
{
  "schemaVersion": 1,
  "error": {
    "code": "NOT_FOUND",
    "message": "The requested resource was not found.",
    "requestId": "85ea7f30f3344ce09a3c7eff8e720cff",
    "retryable": false
  }
}
```

API error codes and request IDs pass through unchanged. Client-only failures use
stable codes such as `DAEMON_UNAVAILABLE`, `API_VERSION_UNSUPPORTED`, and
`API_PROTOCOL_INVALID`; endpoint credentials and raw response bodies are never
included.

## Exit classes

| Code | Class | Typical cases |
|---:|---|---|
| 0 | success | Command completed or idempotent target state already held. |
| 2 | invalid input | Arguments, configuration, or request shape is invalid. |
| 3 | not found | The requested resource does not exist. |
| 4 | conflict | State conflict, failed precondition, or incompatible API version. |
| 5 | input required | A checkpoint or explicit input blocks progress. |
| 6 | provider unavailable | Provider authentication or availability is required. |
| 7 | validation failed | A requested validation completed with findings. |
| 8 | transient failure | Daemon discovery, startup, transport, or retryable execution failed. |
| 10 | invariant violation | A response violated the negotiated protocol or an internal case was impossible. |

API codes take precedence over HTTP status when selecting an exit class. Unknown
valid API failures conservatively map to transient failure; unknown local errors
map to invariant violation so new sibling cases cannot silently appear successful.

## Client retry boundary

The client autostarts only for missing, stale, unauthenticated, or unreachable
endpoint state, then performs one fresh discovery and negotiation. It does not
autostart for version incompatibility or malformed successful responses because a
restart cannot safely repair those contracts. Individual business requests are
not automatically replayed, which prevents an ambiguous transport failure from
duplicating a mutation.
