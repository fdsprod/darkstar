# Daemon lifecycle

The Windows MVP exposes one idempotent per-user lifecycle:

```text
darkstar daemon run
darkstar daemon start
darkstar daemon stop
darkstar daemon restart
darkstar daemon status --json
```

`run` owns an exclusive, held-open `daemon.lock` for its whole foreground
lifetime. `start` launches the same executable in a detached process and waits
until that process publishes verifiable state. Repeated `start` calls succeed
without creating another daemon.

## Identity and stale state

`%LOCALAPPDATA%\DARKSTAR\runtime\daemon.json` contains a schema version, a random
128-bit daemon instance ID, and the process identity:

```json
{
  "schemaVersion": 1,
  "instanceId": "e85fda2b38d142dca92b8bb97f6df180",
  "process": {
    "pid": 1234,
    "startedAt": "2026-08-31T20:00:00.1234Z",
    "executable": "C:\\Program Files\\DARKSTAR\\darkstar.exe"
  }
}
```

The file does not store a `running` flag. Each command derives `running`,
`stale`, or `stopped` by comparing the PID, Windows process creation time, and
canonical executable path. A reused PID therefore cannot authorize signaling or
termination. Stale or malformed state is removed only while holding the daemon
lock, so cleanup cannot race a live instance.

This lifecycle record is separate from the authenticated `endpoint.json` added
with the loopback API. It contains no API token or transport metadata.

## Shutdown

Each daemon instance owns a random, local Windows event. `stop` signals that
exact event and waits five seconds. If the daemon remains alive, the command
opens the recorded process with query and terminate rights, revalidates creation
time and executable identity on that handle, and only then terminates it.

`status --json` returns `schemaVersion: 1`, a stable `status` value, and the
recorded process for `running` and valid `stale` states. Status inspection is
read-only and never autostarts the daemon; `start`, `stop`, and `run` perform
stale cleanup at safe lock boundaries.
