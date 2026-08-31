# Contributing to DARKSTAR

DARKSTAR is Windows-first. Run all commands below from a PowerShell prompt at
the repository root.

## Prerequisites

- Go 1.24 or newer
- Node.js 22.12 or newer
- npm 10 or newer
- Git for Windows

## Clean-checkout setup

Install the pinned dashboard dependencies:

```powershell
./scripts/Bootstrap.ps1
```

Run every deterministic check and produce the CLI/daemon binary plus dashboard
static assets:

```powershell
./scripts/Verify.ps1
```

The binary is written to `out/darkstar.exe`; dashboard assets are written to
`dashboard/dist/`. Both directories are ignored by Git.

For a shorter edit loop, run the phases independently:

```powershell
./scripts/Test.ps1
./scripts/Build.ps1
```

## Repository rules

- Keep deterministic domain behavior in `runtime/src/core`.
- Put interfaces owned by the application in `runtime/src/ports` and concrete
  side-effect implementations in `runtime/src/adapters/<port>/<implementation>`
  or `runtime/src/platform/<os>`.
- Keep transport behavior in `runtime/src/cli`, the future local API package, or
  `dashboard/src`; transports do not own orchestration decisions.
- Add or update tests with behavior changes.
- Keep project-specific guidance in that project's `docs/` directory.
- Keep project tests in that project's `tests/` directory.
- Do not commit `out/`, `node_modules/`, or dashboard `dist/` output.

Project-specific setup and structure are documented in `runtime/docs/` and
`dashboard/docs/`.
