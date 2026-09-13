# SQLite migrations

Migrations are embedded into the runtime and applied in ascending numeric order.
Name files `NNNN_description.sql`, never edit an applied migration, and add only
forward migrations. Each file and its SHA-256 checksum are recorded in
`schema_migrations` in the same transaction as its schema changes.

Keep `schemas/sqlite-v1alpha1.sql` equal to the schema produced after every
embedded migration has run. Migration tests open a fresh database through the
runner and verify that final model, so forward migrations can evolve the schema
without rewriting an applied file.

Migration 0027 introduces authoritative native ticket business records, separate
from execution projections. The `legacy-business-state/v1` mapping copies the
legacy work state once, retains work IDs as native ticket IDs, and records an
immutable history snapshot. Imports or protected external references whose native
identity cannot be established remain `legacy_unresolved`; they are not converted
into native tickets. Existing event, artifact and command bytes remain unchanged.

`native_ticket_history.snapshot_json` stores the concrete `NativeTicket` DTO,
while `request_json` retains each typed operation envelope and progress body.
Legacy snapshots point back to retained events. Tickets, mappings, history and
receipts survive execution projection rebuilds; each native mutation uses a
revision comparison and commits its history and read-back receipt atomically.

Migration 0028 adds immutable project source bindings, original normalized
observations and per-source cache/checkpoints. Page observations and their opaque
cursor commit together; cache loading cannot create execution records.

Migration 0029 adds source lineage, exact observation approvals, independent
source checks and immutable per-run input snapshots. Only native mappings already
present during migration enter `source_legacy_work`; new native work requires
source approval. Explicit admission records `sourceAdmissionId` in its original
work-created event so projection replay cannot fabricate a shadow native ticket.
Source history and snapshots survive projection rebuilds unchanged.
