# DARKSTAR Runtime

The runtime project produces the single `darkstar.exe` binary that hosts both
the command-line interface and local daemon. Its Go module is intentionally
independent from dashboard build tooling.

## Structure

| Path | Responsibility |
|---|---|
| `src/cmd/darkstar` | Executable composition root. |
| `src/cli` | Terminal commands, output, and exit-code mapping. |
| `src/daemon` | Daemon lifecycle and application coordination. |
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

## Validate independently

From the repository root:

```powershell
go -C runtime vet ./...
go -C runtime test ./...
go -C runtime build -o ../out/darkstar.exe ./src/cmd/darkstar
```
