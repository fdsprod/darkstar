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

Run `npm run api:generate --workspace @darkstar/dashboard` after updating the
OpenAPI document. Build and check commands run `api:check`, which fails when the
committed generated surface is stale.
