CREATE TABLE attempt_projection (
  attempt_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  scenario TEXT NOT NULL,
  provider TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('starting', 'running', 'completed', 'failed', 'cancelled', 'interrupted', 'reconcile_required')),
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
  CHECK (status <> 'running' OR provider_thread_id <> '')
) STRICT;

CREATE INDEX attempt_projection_run ON attempt_projection(run_id, created_at, attempt_id);
CREATE INDEX attempt_projection_active ON attempt_projection(status, updated_at);
