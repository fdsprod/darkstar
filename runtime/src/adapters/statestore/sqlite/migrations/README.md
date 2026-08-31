# SQLite migrations

Migrations are embedded into the runtime and applied in ascending numeric order.
Name files `NNNN_description.sql`, never edit an applied migration, and add only
forward migrations. Each file and its SHA-256 checksum are recorded in
`schema_migrations` in the same transaction as its schema changes.

Keep `schemas/sqlite-v1alpha1.sql` equal to the schema produced after every
embedded migration has run. Migration tests open a fresh database through the
runner and verify that final model, so forward migrations can evolve the schema
without rewriting an applied file.
