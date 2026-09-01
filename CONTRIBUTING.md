# Contributing to DARKSTAR

DARKSTAR is Windows-first. Run all commands below from a PowerShell prompt at
the repository root.

## Pinned toolchain

- Go 1.24.0 (see `.go-version`)
- Node.js 22.12.0 (see `.node-version`)
- npm 10.9.0, bundled with Node.js 22.12.0 (see `.npm-version`)
- Git for Windows

The PowerShell entry points reject other tool versions so local and CI builds
use the same compilers and package manager.

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

Create the deterministic Windows ZIP and its SHA-256 sidecar:

```powershell
./scripts/Package.ps1 -Version dev
```

Prove that two clean binary/package builds are byte-for-byte identical:

```powershell
./scripts/Verify-ReproducibleBuild.ps1 -Version dev
```

GitHub Actions runs these commands on a `windows-2025` runner for every
push and pull request. Third-party actions are pinned to immutable commit SHAs.

For a shorter edit loop, run the phases independently:

```powershell
./scripts/Test.ps1
./scripts/Build.ps1
```

## Schema contracts

Workflow, local API, runtime event, provider, and artifact boundaries are
versioned under `schemas/`. Validate every contract and its references, and
verify that the generated catalog is current:

```powershell
node scripts/schema-tool.mjs check
```

After an additive schema edit, regenerate the catalog and rerun the check:

```powershell
node scripts/schema-tool.mjs generate
node scripts/schema-tool.mjs check
```

To reproduce the CI compatibility gate against another Git revision:

```powershell
node scripts/schema-tool.mjs compatibility --base origin/main
```

Do not modify a published version in a way that removes an API operation,
property, enum value, accepted type, or response; adds a new requirement; or
tightens a validation bound. Keep the old contract and add a newly versioned
file for those changes.

## Repository rules

Before implementing work governed by an architecture decision, identify the
applicable DS keys and run the supersession preflight:

```powershell
node scripts/governance-reference.mjs docs/decisions/decision-register.json docs/risks/risk-register.json DS-004 DS-010
```

Read the current canonical documents and surfaced risks before changing code.
When work becomes affected by a decision, add its stable DS key to that decision's
`affectedIssues` entry. New decisions and risk dispositions must follow the
[decision](docs/decisions/README.md) and [risk-register](docs/risks/README.md)
conventions.

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
