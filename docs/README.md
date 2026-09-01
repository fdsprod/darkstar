# DARKSTAR documentation

This directory separates documentation by the kind of decision or work it represents. Normative contracts live with architecture, product intent lives with product documentation, accepted technical choices live under decisions, and delivery sequencing lives under planning.

## Product

- [Product and technical specification](product/product-specification.md) — goals, requirements, architecture overview, user experience, and MVP scope.
- [Default software-delivery workflow](product/default-workflow.md) — the shipped workflow, checkpoints, artifacts, and route behavior.

## Architecture

### Workflow runtime

- [Workflow execution semantics](architecture/workflow/execution-semantics.md) — normative graph, binding, gate, transition, join, retry, checkpoint, and error behavior.
- [Artifact and context contract](architecture/artifacts/ARTIFACT_AND_CONTEXT_CONTRACT.md) — immutable ingest, support matrix, representations, binding, selection budgets, and safe degradation.
- [MVP work and Git model](architecture/work/WORK_AND_GIT_MODEL.md) — normative work identity, delivery topology, mutation ownership, revision, and recovery behavior.
- [Crash recovery and idempotency model](architecture/recovery/RECOVERY_MODEL.md) — normative leases, process identity, interruption matrix, commit points, reconciliation, and failure-injection contract.
- [Windows platform contract](architecture/platform/WINDOWS_PLATFORM_CONTRACT.md) — paths, locks, endpoint discovery, atomic state, Job Object ownership, shutdown, ConPTY, and support matrix.
- [Runtime contract](architecture/runtime/RUNTIME_CONTRACT.md) — stable resources and events, command transactions, SQLite model, projections, API, SSE replay, pagination, and CLI errors.
- [Core ports and adapter package rules](architecture/runtime/PORTS_AND_ADAPTERS.md) — provider-neutral interfaces, concrete implementation layout, and enforced dependency direction.

### Security and authorization

- [Approval and permission model](architecture/security/APPROVAL_AND_PERMISSION_MODEL.md) — normative approval classes, scope, actors, expiry, session grants, offline behavior, audit, and idempotent decision API.
- [MVP threat model](architecture/security/THREAT_MODEL.md) — trust boundaries, high-risk threats, owned backlog controls, priorities, negative tests, and residual risk.

### Provider integration

- [Codex adapter contract](architecture/providers/codex/adapter-contract.md) — normalized provider boundary used by DARKSTAR core.
- [Codex compatibility policy](architecture/providers/codex/compatibility-policy.md) — executable selection, supported versions, capability negotiation, and fixture policy.
- [Capability registry contract](architecture/capabilities/CAPABILITY_REGISTRY_CONTRACT.md) — guaranteed, registered, inherited, and unsupported capabilities; skill/tool fingerprints; policy; fallback; and audit.

## Decisions

- [DS-001 Codex host recommendation](decisions/DS-001-codex-host-recommendation.md) — accepted transport choice and supporting evidence.

Future architecture decisions should use one file per decision in this directory and identify the related DS issue in the filename and document header.

## Planning

- [MVP backlog](planning/mvp-backlog.md) — dependencies, spikes, initiatives, acceptance coverage, and the suggested first iteration.

## Executable contracts and evidence

Some project knowledge belongs beside executable material rather than in `docs/`:

- [`schemas/`](../schemas/) contains machine-readable contracts.
- [`examples/`](../examples/) contains workflow and scenario examples.
- [`tests/`](../tests/) verifies deterministic behavior.
- [`examples/recovery/`](../examples/recovery/) and [`scripts/recovery-reference.mjs`](../scripts/recovery-reference.mjs) make crash-window decisions and catalog coverage executable.
- [`examples/approvals/`](../examples/approvals/) and [`scripts/approval-reference.mjs`](../scripts/approval-reference.mjs) make approval separation, negative cases, and idempotency executable.
- [`examples/artifacts/`](../examples/artifacts/) and [`scripts/artifact-reference.mjs`](../scripts/artifact-reference.mjs) provide the self-contained golden artifact corpus and deterministic context selector.
- [`examples/repositories/`](../examples/repositories/) and [`scripts/repository-fixtures.mjs`](../scripts/repository-fixtures.mjs) materialize deterministic clean, dirty, branched, and linked-worktree Git fixtures.
- [`examples/runtime/`](../examples/runtime/) and [`scripts/runtime-reference.mjs`](../scripts/runtime-reference.mjs) validate the fake end-to-end event trace and projections.
- [`examples/capabilities/`](../examples/capabilities/) and [`scripts/capability-reference.mjs`](../scripts/capability-reference.mjs) make capability classification, resolution, and fallback executable.
- [`examples/security/`](../examples/security/) and [`scripts/threat-model-reference.mjs`](../scripts/threat-model-reference.mjs) enforce owned control and negative-test coverage for every high-risk trust boundary.
- [`probes/codex-host/`](../probes/codex-host/) contains the Codex host probe harness and versioned compatibility evidence.
- [`probes/windows-process-control/`](../probes/windows-process-control/) proves the DS-009 Windows lifecycle and owned process-tree boundary.

Those directories may include local README files explaining how to run or extend their contents.
