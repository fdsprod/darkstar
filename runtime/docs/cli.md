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
code, and required action. `--json` returns the versioned report unchanged. A
degraded or unhealthy report uses exit class 7 because the diagnostic completed
successfully with findings; transport failures retain their ordinary exit class.

## Machine output

Every finite human-readable command accepts `--json` as its final argument. A
successful command writes exactly one JSON value to stdout and includes
`"schemaVersion": 1`; it writes no progress prose to either stream. A failed
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
