# DARKSTAR TypeScript plugin SDK

This SDK implements TypeScript resource, tool, node, and provider contributions.
The built-in packages use these contracts for journals and workspace tools,
supported node behavior, and the Codex connector. Content processors and storage
backends retain their existing Go ports.

`src/index.ts` exports `Tool`, `Node`, `Resource`, `Plugin`, `HostServices`, and `serve`.
Tool schemas and handlers are provider-neutral. A resource declares its create
and update operations; the daemon independently enforces these operations and
owns durable storage, idempotency, and resource binding. A decision-log record
does not grant an approval.

Node contributions use the bounded invocation transport to construct agent tasks,
refine declared output schemas, and execute deterministic work through scoped host
services. The daemon authorizes every requested operation against the original
configuration. Git preparation and ownership, mandatory checks, gates, approvals,
input binding, retries, and transitions remain daemon-owned. A plugin's candidate
output cannot replace evidence that required host operations completed.

`src/provider.ts` exports `ProviderPlugin`, `ProviderHost`, and `serveProvider`.
Provider contributions implement the persistent connector lifecycle: probe,
capabilities, start/resume, normalized events, results, interaction responses, and
cancellation. The Codex package translates these operations to app-server RPC and
routes dynamic tool requests through daemon host callbacks.

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

Resource, tool, and node exchanges launch fresh processes. `describe` returns a descriptor;
`invoke` receives `{contribution, arguments}`. Stdout belongs exclusively to this
protocol. Log to stderr. The Go host bounds messages to 1 MiB, retained stderr to
8 KiB, callbacks to 128, and process lifetime to 30 seconds by default. Context
cancellation terminates the immediate plugin process. This is not a security
sandbox or process-tree containment; explicitly composed plugins are trusted
native code with the operating-system rights of the daemon account. Provider
contributions instead keep a persistent process for their lifecycle operations;
the provider host retains process ownership and cancellation handling.

## Built-in packages

- `plugins/builtin-resources/src/index.ts`: open items, decision logs, and workspace tools.
- `plugins/builtin-nodes/src/index.ts`: reasoning, implementation, point execution,
  command, workspace preparation, and workspace validation.
- `plugins/provider-codex/src/index.ts`: Codex app-server connector behavior.

Build and check all self-contained embedded JavaScript artifacts:

```powershell
npm --prefix packages/plugin-sdk ci
npm --prefix packages/plugin-sdk run build
npm --prefix packages/plugin-sdk run check
go -C runtime test ./src/adapters/plugin/process
go -C runtime test ./src/adapters/nodeextension/typescript
```

The standalone package lock pins its TypeScript, Node 22 type definitions,
esbuild, and Acorn development dependencies without changing the application packages.
Both `build` and `check` enforce multiline braced control flow and one statement
per line using the installed TypeScript compiler's AST, then run strict TypeScript
checking across every package. `check` also runs lint fixtures and all Codex
behavioral fixtures, and verifies that every committed generated bundle is current.
`scripts/Build.ps1` enforces the same source lint before checking bundle freshness.

The generators transpile each package with its SDK implementation using esbuild;
runtime imports are Node built-ins. Syntax-tree checks reject unbundled application
dependencies, `require`, and dynamic imports before bundling.
The committed output is embedded in the Go daemon, materialized in its private
data directory, and checked against its SHA-256 digest at every process start.
Building the daemon does not require running the TypeScript generator; executing
plugin contributions requires Node. New runs pin their built-in package identities;
legacy unpinned runs retain their existing execution path. Missing or changed pinned
implementations fail rather than silently selecting another implementation.

Third-party installation, dependency verification for arbitrary packages,
permission sandboxing, and TypeScript migration of other extension families are
separate work. This package is currently private and consumed from source; it is
not an npm publication or a supported general package installer.
