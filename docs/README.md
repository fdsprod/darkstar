# DARKSTAR documentation

This directory separates documentation by the kind of decision or work it represents. Normative contracts live with architecture, product intent lives with product documentation, accepted technical choices live under decisions, and delivery sequencing lives under planning.

## Product

- [Product and technical specification](product/product-specification.md) — goals, requirements, architecture overview, user experience, and MVP scope.
- [Default software-delivery workflow](product/default-workflow.md) — the shipped workflow, checkpoints, artifacts, and route behavior.

## Architecture

### Workflow runtime

- [Workflow execution semantics](architecture/workflow/execution-semantics.md) — normative graph, binding, gate, transition, join, retry, checkpoint, and error behavior.

### Provider integration

- [Codex adapter contract](architecture/providers/codex/adapter-contract.md) — normalized provider boundary used by DARKSTAR core.
- [Codex compatibility policy](architecture/providers/codex/compatibility-policy.md) — executable selection, supported versions, capability negotiation, and fixture policy.

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
- [`probes/codex-host/`](../probes/codex-host/) contains the Codex host probe harness and versioned compatibility evidence.

Those directories may include local README files explaining how to run or extend their contents.
