# SQLite migrations

Migrations are embedded into the runtime and applied in ascending numeric order.
Name files `NNNN_description.sql`, never edit an applied migration, and add only
forward migrations. Each file and its SHA-256 checksum are recorded in
`schema_migrations` in the same transaction as its schema changes.

Keep `schemas/sqlite-v1alpha1.sql` equal to the initial executable migration so
the published logical model and fresh database schema cannot drift.
