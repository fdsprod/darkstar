# DARKSTAR Dashboard

The dashboard is a thin React and TypeScript client for DARKSTAR's versioned
local API. It owns presentation and browser interaction state; workflow and
policy decisions remain in the runtime.

## Structure

| Path | Responsibility |
|---|---|
| `src` | React components, browser state, styles, and future API clients. |
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
