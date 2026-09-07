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

Run `npm run api:generate --workspace @darkstar/dashboard` after updating the
OpenAPI document. Build and check commands run `api:check`, which fails when the
committed generated surface is stale.
