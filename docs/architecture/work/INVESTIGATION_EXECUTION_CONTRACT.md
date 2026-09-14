# Repository investigation execution

DAR-158 implements a bounded daemon-owned collection over a DAR-156 immutable
repository scope. It does not add a workflow loop language, approve artifacts,
publish stories, or grant implementation/delivery authority.

## Frozen inputs and work units

Preparation records the exact scope digest, selected repository IDs, task,
provider implementation and capability fingerprint, and collection concurrency.
The task is explicit text or an exact feature brief artifact ID/version/SHA-256.
Briefs pass their versioned schema and semantic reference checks. Their original
bytes remain in the artifact registry; the frozen task is a canonical JSON
projection with its own digest, separate from the original source SHA-256.

Each selected repository has one stable work unit. One synthesis unit consumes
accepted repository findings and explicit missing/failed repository evidence.
The daemon claims units, records attempt history, validates submissions, and
decides advancement. An execution agent receives only the named task, its own
repository manifest or the accepted findings, the granted tools, and a structured
output contract. It receives no workflow graph or scheduling instructions.

## Scope and provider authority

Repository attempts bind only their selected, retained, integrity-verified
snapshot. Aggregate scope preparation can be blocked while an individual retained
subset remains usable. Missing evidence cannot become ambient checkout access.
Synthesis binds an explicit empty filesystem scope. No-code collections use a
deterministic daemon attempt that records `no_repository_evidence`; they do not
require a provider or invent repository findings.

The runner requires the provider's exact supported scoped-read capability and
fingerprint before dispatch. It denies network access, commands, file mutations,
and unrelated tools. The bundled native and TypeScript Codex adapters currently
cannot enforce selected read roots and therefore reject these attempts. A generic
read-only mode or a prompt is insufficient. Adding an isolated execution backend
is a separate capability requirement; these changes do not claim to provide it.

## Durable execution and recovery

The complete prepared request and its digest are retained before provider start.
Handles, ordered events, submissions, and artifact results are durable observations.
Only validated `submit_output` content can satisfy a reasoning unit; final model
prose or a success claim cannot do so. Citation validation checks the selected
repository, exact commit and blob, retained regular-file path, and line bounds.
Synthesis must retain the exact accepted artifact versions and every missing unit.

The SQLite claim transaction enforces collection and investigation capacity.
Daemon composition shares an admission lock and occupied-slot observations with
the workflow queue, using the configured global run limit. Live workers and
uncertain ownership continue to consume capacity. Claims have renewable leases;
expiry permits reconciliation, never duplicate dispatch. Cancellation closes new
admission before asking active providers to stop. Unconfirmed starts or stops
remain uncertain and cannot be retried as fresh executions.

Retry retains successful repository units and their artifacts. It retries eligible
unsuccessful units and creates a new synthesis attempt while preserving previous
attempts and synthesis artifacts. Restart reuses stored successful results.

## Artifact identity and compatibility

Findings and synthesis are immutable registry artifacts with exact investigation
collection/unit/attempt provenance. Storage uses an immutable provenance subtype
alongside the common artifact version row, inserted in the same transaction.
Canonical readers retain that subtype across listing and projection rebuilds;
no workflow run identity is fabricated.

The new artifact read contract is versioned. Legacy artifact responses retain their
closed provenance union; investigation artifacts are available through investigation
results and the `artifacts-v2` read surface. Existing artifacts, revisions, and raw
history remain unchanged. See [runtime commands](../../../runtime/docs/investigations.md)
and the [feature planning contract](../artifacts/FEATURE_PLANNING_CONTRACT.md).
