# DARKSTAR

DARKSTAR is a local-first orchestration runtime for durable, auditable software-delivery workflows. It keeps workflow control deterministic while allowing reasoning providers to produce structured assessments and artifacts.

The project is currently in the contract and walking-skeleton phase. The repository contains the normative product and architecture specifications, executable workflow examples, a reference interpreter, and captured Codex host compatibility evidence.

## Start here

- [Documentation index](docs/README.md)
- [Product specification](docs/product/product-specification.md)
- [Default software-delivery workflow](docs/product/default-workflow.md)
- [Workflow execution semantics](docs/architecture/workflow/execution-semantics.md)
- [Crash recovery and idempotency model](docs/architecture/recovery/RECOVERY_MODEL.md)
- [Approval and permission model](docs/architecture/security/APPROVAL_AND_PERMISSION_MODEL.md)
- [Artifact and context contract](docs/architecture/artifacts/ARTIFACT_AND_CONTEXT_CONTRACT.md)
- [Windows platform contract](docs/architecture/platform/WINDOWS_PLATFORM_CONTRACT.md)
- [Runtime event, API, and persistence contract](docs/architecture/runtime/RUNTIME_CONTRACT.md)
- [MVP backlog](docs/planning/mvp-backlog.md)

## Repository layout

| Path | Purpose |
|---|---|
| [`docs/`](docs/) | Product, architecture, decisions, and planning documentation |
| [`schemas/`](schemas/) | Machine-readable workflow and route-patch contracts |
| [`examples/`](examples/) | Valid workflows, route patches, and execution scenarios |
| [`scripts/`](scripts/) | Executable reference and project utilities |
| [`tests/`](tests/) | Deterministic contract tests |
| [`probes/`](probes/) | Provider and host compatibility probes with versioned evidence |

## Verify the current contracts

From the repository root:

```powershell
node --test tests/workflow-reference.test.mjs
node scripts/recovery-reference.mjs examples/recovery/recovery-scenarios.json
node --test tests/recovery-reference.test.mjs
node scripts/approval-reference.mjs examples/approvals/approval-scenarios.json
node --test tests/approval-reference.test.mjs
node scripts/artifact-reference.mjs examples/artifacts/golden-corpus.json
node --test tests/artifact-reference.test.mjs
./probes/windows-process-control/Test-WindowsProcessProbe.ps1
node scripts/runtime-reference.mjs examples/runtime/fake-run.json
node --test tests/runtime-reference.test.mjs
./probes/codex-host/Test-CodexHostFixtures.ps1
```

Production daemon and CLI code begins with DS-020, the repository and module-boundary initialization item.
