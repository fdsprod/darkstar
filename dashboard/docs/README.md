# DARKSTAR Dashboard

The dashboard is a thin React and TypeScript client for DARKSTAR's versioned
local API. It owns presentation and browser interaction state; workflow and
policy decisions remain in the runtime.

## Structure

| Path | Responsibility |
|---|---|
| `src` | React components, browser state, styles, and future API clients. |
| `src/app` | Application boundary and dependency-free browser router. |
| `src/api` | Typed client and schema-generated types for the versioned local API. |
| `src/components` | Reusable shell and navigation primitives. |
| `src/pages` | Route-level dashboard views. |
| `tests` | Dashboard behavior and structural contract tests. |
| `index.html` | Browser entry document. |
| `vite.config.ts` | Production asset build configuration. |
| `package.json` | Project dependencies and contribution commands. |

## Validate independently

After running the root `scripts/Bootstrap.ps1` command:

```powershell
npm run check --workspace @darkstar/dashboard
npm run build --workspace @darkstar/dashboard
```

The production output is written to `dashboard/dist/` and is not committed.

## Browser contract

The daemon injects `window.__DARKSTAR_BOOTSTRAP__` before the module script. The
client synchronously consumes its API version and authorization header into
module-private memory and removes the global. It never writes that credential to
a URL or browser storage. Requests use relative `/api/v1` paths.

## Settings contract

Settings is organized into General, Projects, Providers, Execution &
permissions, and Health. Editable controls are derived from the server catalog;
each control keeps its effective value and source, validation result, restart
impact, and save action together. Reset is a typed unset mutation that is
previewed, applied with revision protection, and followed by an authoritative
state refresh.

Health guidance, provider executable details, project identifiers and source
fingerprints, configuration revisions, and recovery receipts are disclosed only
when requested. Secret values continue to use the write-only secret operation
and never enter React state. Legacy `configuration` and `provider` tab links are
mapped to their current sections, while setting deep links select the section
owned by the catalog key.

## Board lifecycle contract

Board is the default operational surface. Its eight columns begin with the
newest work and run projections, then use the lifecycle plan's state when that
fuller projection arrives. Each card separately loads the exhaustive transition
plan from the daemon. A cached plan is ignored when its composite resource
version is older than the card's newest work or run projection. Drag
destinations and the Move or Start menu
use only targets marked `enabled`; disabled menu rows keep the daemon reason
visible in text. All input methods submit the same transition body with the
plan's composite resource version and a fresh idempotency key.

Cards do not move optimistically. They remain in their projected column while a
command is pending and move only after the authoritative collections refresh.
A rejection refreshes the collections, and a version conflict also replaces the
card's transition plan from the error response. SSE events are ordered,
deduplicated invalidation signals: reconnect replay can refresh the board and
recent activity, but never creates a local lifecycle state.

The quick panel combines the durable requested outcome, current lifecycle plan,
newest run, and recent event buffer. It links to work, run, readiness, and
evidence pages for full context rather than duplicating those workflows on the
board.

## Full work context

Board cards and checkpoint deep links open a focused work view. Work and run
tabs are encoded in the URL, so reload, back, and forward restore Overview,
Execution, Agents and permissions, Evidence, Activity, or Diagnostics without
copying domain state into the browser. The run-scoped agent workspace reuses the
same server-authorized cancellation and provider-permission controls as the
global diagnostic route, including bounded live-log cursors and resource-version
refresh after conflicts.

Artifacts are read through their exact work, story, implementation-point, run,
and node bindings. The context view shows immutable revisions, recorded
provenance, and freshness impact beside the owner; the global artifact and agent
routes remain supported for deep links and diagnostics but are not primary
navigation destinations. Human-readable activity stays compact while raw event
positions, aggregate identifiers, resource versions, and route digests remain in
Diagnostics.

Run `npm run api:generate --workspace @darkstar/dashboard` after updating the
OpenAPI document. Build and check commands run `api:check`, which fails when the
committed generated surface is stale.
