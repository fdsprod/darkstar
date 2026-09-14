# Contributing to DARKSTAR

DARKSTAR is Windows-first. Run all commands below from a PowerShell prompt at
the repository root.

## Toolchain defaults and minimums

- Go 1.24.0 (see `.go-version`)
- golangci-lint 2.8.0 (see `.golangci-version`)
- Node.js 22.12.0 (see `.node-version`)
- npm 10.9.0, bundled with Node.js 22.12.0 (see `.npm-version`)
- Git for Windows

Go, Node.js, and npm checks accept these versions or newer stable versions.
Setup and CI install the listed defaults; the linter remains pinned.
The daemon and scripts share a verified local tool binding so stale PATH entries
cannot select different installations. See [project toolchain setup](runtime/docs/toolchains.md)
for registration, repair, and cache behavior.

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

The full verification phases can also be run independently:

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
suites. `Verify.ps1` adds browser acceptance tests and the full build and is the
command used by Windows CI. Neither command is the default for every local edit.

If only the linter is missing or its pinned version changed, install it with:

```powershell
./scripts/Install-Lint.ps1
```

## Test selection

During development, select tests by the behavior and dependencies changed. Run
the affected unit/contract tests first, then integration or end-to-end checks
that exercise the changed boundaries. Include consumers of changed interfaces;
testing only the package where a type is defined is insufficient.

| Change | Local checks |
| --- | --- |
| Isolated runtime behavior | Changed Go package and relevant consumer tests |
| Git, SQLite, scheduling, or permissions | Relevant integration/negative tests; include migration or recovery tests when those behaviors change |
| Daemon lifecycle or API/CLI wiring | Focused daemon/CLI acceptance scenario plus affected transport tests |
| Dashboard behavior | Dashboard type checks and unit tests; browser specs for affected interactions, routing, rendering, or API bindings |
| Shared UI primitives, styles, or Storybook setup | Relevant browser specs; full browser or Storybook catalog checks when impact spans the application |
| Schemas or provider protocols | Contract, compatibility, and affected consumer/adapter checks |
| Documentation only | Check links, content, and diff; run relevant validation if the document is an executable input |

For a connected sequence of issues, run focused checks for each atomic change
and one full regression pass at the integration checkpoint before handoff.
Broaden earlier for shared contracts, migrations, provider/security boundaries,
toolchain or dependency changes, or unclear impact. Do not rerun a passing check
just because a new commit was made: reuse its result until relevant source,
dependencies, configuration, or the test environment changes. After a failure,
rerun the affected checks after fixing it; widen coverage if the failure exposes
a broader problem. Full CI verification remains required.

Load the project toolchain once in the PowerShell session:

```powershell
. ./scripts/Use-ProjectToolchain.ps1
```

Go already supports selecting packages and individual scenarios. For example:

```powershell
# Runner behavior and output contracts, without unrelated runtime packages.
go -C runtime test -mod=readonly ./src/core/investigationrunner ./tests/investigationrunner

# Snapshot and investigation integration tests, including real Git/SQLite work.
go -C runtime test -mod=readonly ./tests/repositoryscope

# One daemon/CLI/restart acceptance scenario, when that boundary changes.
go -C runtime test -mod=readonly ./src/cli -run '^TestNoCodeInvestigationPersistsArtifactAcrossDaemonRestart$'
```

Check that a filtered command actually selects tests; Go can succeed with
`[no tests to run]`. `-run` limits test execution but still compiles the selected
package and its dependencies. Avoid `-count=1` unless an uncached run is needed;
Go normally reuses eligible passing results. The current suite does not use
`testing.Short()`, so adding `-short` does not skip integration or end-to-end work.

Dashboard checks and browser acceptance checks are separate:

```powershell
# Generated API freshness, type checks, and Node unit tests; no browser startup.
npm run check

# Only the affected browser spec, with API fixtures.
npm run test:browser -- dashboard/e2e/project-repositories.mutations.spec.ts

# Full browser acceptance suite, for changes with broad UI impact.
npm run test:browser

# Full Storybook catalog, for changes affecting shared component rendering.
npm run test:stories
```

Browser tests with API fixtures validate UI behavior, not a live daemon/provider
deployment. Go tests include both unit and integration tests, and some CLI tests
exercise the daemon end to end. Choose by the boundary under test rather than
assuming all Go tests are cheap or all browser tests use a full deployment.

Record which checks ran and any known failures or gaps. A targeted pass is not
evidence that the full suite passed. Existing unrelated failures should be
reported separately, without repeated full runs when nothing relevant changed.

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
