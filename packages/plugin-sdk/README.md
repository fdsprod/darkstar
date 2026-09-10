# DARKSTAR TypeScript plugin SDK

This initial SDK implements resource and tool contributions. Nodes, provider
connectors, content processors, and storage backends retain their existing Go
ports; they are not yet external TypeScript contribution families.

`src/index.ts` exports `Tool`, `Resource`, `Plugin`, `HostServices`, and `serve`.
Tool schemas and handlers are provider-neutral. A resource declares its create
and update operations; the daemon independently enforces these operations and
owns durable storage, idempotency, and resource binding. A decision-log record
does not grant an approval.

Journal reads return a revision. Mutations must supply `expectedRevision` and a
stable operation key; stale writes fail, while identical retries reuse the
original receipt. Workspace reads return a content digest. Writes create a new
name unless they supply `expectedDigest` for a checked replacement. Publishing
snapshots the staged bytes into an immutable artifact version; later replacements
cannot alter the published version.

The daemon explicitly composes a plugin host with a trusted Node executable,
absolute entrypoint, exact version/digest, and granted host-service methods.
Workflows cannot supply executable paths or increase grants. Plugin code calls
`host.call(method, arguments)` with handles bound by the daemon, never database
paths or run owners. The process host validates argument and result schemas and
intersects requested callbacks with the tool's declared and host-granted methods.

The wire protocol is newline-delimited JSON using `darkstar.plugin/v1`:

- `request`: `id`, `method` (`describe` or `invoke`), `params`.
- `response`: matching `id`, either `result` or `error` (string).
- `host_call`: unique `id`, `method`, `params`.
- `host_result`: matching callback `id`, either `result` or `error`.

Each exchange launches a fresh process. `describe` returns a descriptor;
`invoke` receives `{contribution, arguments}`. Stdout belongs exclusively to this
protocol. Log to stderr. The Go host bounds messages to 1 MiB, retained stderr to
8 KiB, callbacks to 128, and process lifetime to 30 seconds by default. Context
cancellation terminates the immediate plugin process. This is not a security
sandbox or process-tree containment; explicitly composed plugins are trusted
native code with the operating-system rights of the daemon account.

## Built-in package

`plugins/builtin-resources/src/index.ts` implements open items, decision logs,
and workspace tools. Build the self-contained embedded JavaScript artifact:

```powershell
npm --prefix packages/plugin-sdk ci
npm --prefix packages/plugin-sdk run build
npm --prefix packages/plugin-sdk run check
go -C runtime test ./src/adapters/plugin/process
```

The standalone package lock pins its TypeScript, Node 22 type definitions,
esbuild, and Acorn development dependencies without changing the application packages.
Both `build` and `check` run strict TypeScript checking across the SDK and built-in
source. `check` also verifies that the committed generated bundle is current.

The generator transpiles the SDK and built-in source together using esbuild;
its only runtime import is Node's `readline`. Syntax-tree checks reject changed
imports, re-exported dependencies, `require`, and dynamic imports before bundling.
The committed output is embedded in the Go daemon, materialized in its private
data directory, and checked against its SHA-256 digest at every process start.
Building the daemon does not require running the TypeScript generator; executing
plugin tools requires Node. The daemon controls behavior when Node is unavailable.

Third-party installation, dependency verification for arbitrary packages,
permission sandboxing, and TypeScript migration of other extension families are
separate work. This package is currently private and consumed from source; it is
not an npm publication or a supported general package installer.
