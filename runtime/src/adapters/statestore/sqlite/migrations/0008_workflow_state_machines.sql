PRAGMA defer_foreign_keys = ON;

CREATE UNIQUE INDEX events_aggregate_command ON events(aggregate_id, command_id);

ALTER TABLE run_projection RENAME TO run_projection_v1;

CREATE TABLE run_projection (
  run_id TEXT PRIMARY KEY,
  work_item_id TEXT NOT NULL,
  workflow_id TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('draft', 'ready', 'queued', 'running', 'waiting', 'blocked', 'completed', 'failed', 'cancelled', 'reconcile_required')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

INSERT INTO run_projection
SELECT run_id, work_item_id, workflow_id, workflow_version,
  CASE status WHEN 'pending' THEN 'draft' ELSE status END,
  resource_version, last_global_position, created_at, updated_at
FROM run_projection_v1;

DROP TABLE run_projection_v1;

DROP INDEX attempt_projection_run;
DROP INDEX attempt_projection_active;
ALTER TABLE attempt_projection RENAME TO attempt_projection_v1;

CREATE TABLE attempt_projection (
  attempt_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  visit_id TEXT NOT NULL DEFAULT '',
  node_id TEXT NOT NULL,
  scenario TEXT NOT NULL,
  provider TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('created', 'starting', 'running', 'validating', 'succeeded', 'failed', 'cancelled', 'interrupted', 'reconcile_required')),
  provider_thread_id TEXT NOT NULL DEFAULT '',
  provider_turn_id TEXT NOT NULL DEFAULT '',
  process_owner_id TEXT NOT NULL DEFAULT '',
  last_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
  log_reference TEXT NOT NULL DEFAULT '',
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (provider_thread_id = '' AND provider_turn_id = '' AND process_owner_id = '' AND last_sequence = 0) OR
    (provider_thread_id <> '' AND provider_turn_id <> '' AND process_owner_id <> '')
  ),
  CHECK (status NOT IN ('running', 'validating', 'succeeded') OR provider_thread_id <> '')
) STRICT;

INSERT INTO attempt_projection
SELECT attempt_id, run_id, '', node_id, scenario, provider,
  CASE status WHEN 'completed' THEN 'succeeded' ELSE status END,
  provider_thread_id, provider_turn_id, process_owner_id, last_sequence, log_reference,
  resource_version, last_global_position, created_at, updated_at
FROM attempt_projection_v1;

DROP TABLE attempt_projection_v1;

CREATE INDEX attempt_projection_run ON attempt_projection(run_id, created_at, attempt_id);
CREATE INDEX attempt_projection_active ON attempt_projection(status, updated_at);

CREATE TABLE node_projection (
  visit_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'ready', 'running', 'validating', 'waiting_checkpoint', 'succeeded', 'rejected', 'failed', 'cancelled')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX node_projection_run ON node_projection(run_id, created_at, visit_id);
