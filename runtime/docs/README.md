# DARKSTAR Runtime

The runtime project produces the single `darkstar.exe` binary that hosts both
the command-line interface and local daemon. Its Go module is intentionally
independent from dashboard build tooling.

## Structure

| Path | Responsibility |
|---|---|
| `src/cmd/darkstar` | Executable composition root. |
| `src/cli` | Terminal commands, output, and exit-code mapping. |
| `src/api` | Authenticated, versioned loopback HTTP transport and endpoint discovery. |
| `src/daemon` | Daemon lifecycle and application coordination. |
| `src/doctor` | Safe subsystem readiness probes and actionable diagnostics. |
| `src/core` | Deterministic domain and workflow behavior. |
| `src/ports` | Application-owned interfaces for external effects. |
| `src/adapters` | Infrastructure implementations of runtime ports. |
| `src/adapters/provider/fake` | Deterministic provider scenarios for stream, interaction, recovery, failure, and cancellation tests. |
| `src/platform` | Operating-system strategy implementations. |
| `tests` | Black-box and cross-package runtime tests. |

At a high level, deterministic behavior belongs in `core`, external contracts in
`ports`, and concrete side effects in `adapters` or `platform`.

See the normative [core ports and adapter package rules](../../docs/architecture/runtime/PORTS_AND_ADAPTERS.md)
for the six port families and the dependency checks enforced by `go test`.

See [runtime configuration](configuration.md) for layer precedence, source
attribution, file locations, and Windows application-data roots.

See [daemon lifecycle](daemon-lifecycle.md) for foreground/background commands,
identity verification, stale-state handling, and shutdown escalation.

See [authenticated loopback API](loopback-api.md) for endpoint discovery, bearer
token rotation, version negotiation, and stable HTTP errors.

See [CLI API client and machine output](cli.md) for daemon autostart, authenticated
transport, versioned JSON conventions, and stable process exit classes.

See [durable coordination](coordination.md) for lease fencing, heartbeat and
release semantics, deterministic queues, and repository writer locks.

See [startup reconciliation](startup-recovery.md) for authority-backed recovery
decisions, durable evidence, scheduler gating, and API/CLI observability.

## Validate independently

From the repository root:

```powershell
go -C runtime vet ./...
go -C runtime test ./...
go -C runtime build -o ../out/darkstar.exe ./src/cmd/darkstar
```
