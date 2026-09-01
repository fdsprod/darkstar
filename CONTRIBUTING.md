# Contributing to DARKSTAR

DARKSTAR is Windows-first. Run all commands below from a PowerShell prompt at
the repository root.

## Pinned toolchain

- Go 1.24.0 (see `.go-version`)
- golangci-lint 2.8.0 (see `.golangci-version`)
- Node.js 22.12.0 (see `.node-version`)
- npm 10.9.0, bundled with Node.js 22.12.0 (see `.npm-version`)
- Git for Windows

The PowerShell entry points reject other tool versions so local and CI builds
use the same compilers and package manager.

## Clean-checkout setup

Install the pinned linter and dashboard dependencies:

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
./scripts/Format.ps1
./scripts/Lint.ps1
./scripts/Test.ps1
./scripts/Build.ps1
```

`Format.ps1` is the canonical formatting command. It runs standard `gofmt` over
every tracked `.go` file. Run it before committing Go changes; a second run must
produce no diff. Configure your editor to run `gofmt` on save (for VS Code with
the Go extension, set `go.formatTool` to `gofmt` and enable
`editor.formatOnSave` for Go files).

`Lint.ps1` applies the repository's minimal `.golangci.yml` baseline to every
tracked Go module: `govet`, `staticcheck`, `errcheck`, `ineffassign`, and
`unused`. `Test.ps1` is the canonical local check command and fails with an
actionable `Format.ps1` hint when any tracked Go file is not canonical. It also
runs the same linter configuration before the Go, contract, and dashboard test
suites. `Verify.ps1` adds the full build and is the command used by Windows CI.

If only the linter is missing or its pinned version changed, install it with:

```powershell
./scripts/Install-Lint.ps1
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
