CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY CHECK (version > 0),
  name TEXT NOT NULL UNIQUE,
  checksum TEXT NOT NULL CHECK (length(checksum) = 64),
  applied_at TEXT NOT NULL
) STRICT;

CREATE TABLE global_positions (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  last_position INTEGER NOT NULL CHECK (last_position >= 0)
) STRICT;

INSERT INTO global_positions(singleton, last_position) VALUES (1, 0);

CREATE TABLE aggregates (
  aggregate_id TEXT PRIMARY KEY,
  aggregate_type TEXT NOT NULL CHECK (aggregate_type IN ('project', 'work', 'run', 'visit', 'attempt', 'artifact', 'approval', 'operation')),
  revision INTEGER NOT NULL CHECK (revision >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (aggregate_type = 'project' AND aggregate_id GLOB 'project_*') OR
    (aggregate_type = 'work' AND aggregate_id GLOB 'work_*') OR
    (aggregate_type = 'run' AND aggregate_id GLOB 'run_*') OR
    (aggregate_type = 'visit' AND aggregate_id GLOB 'visit_*') OR
    (aggregate_type = 'attempt' AND aggregate_id GLOB 'attempt_*') OR
    (aggregate_type = 'artifact' AND aggregate_id GLOB 'artifact_*') OR
    (aggregate_type = 'approval' AND aggregate_id GLOB 'approval_*') OR
    (aggregate_type = 'operation' AND aggregate_id GLOB 'operation_*')
  )
) STRICT;

CREATE TABLE events (
  global_position INTEGER PRIMARY KEY CHECK (global_position > 0),
  event_id TEXT NOT NULL UNIQUE,
  schema_version INTEGER NOT NULL CHECK (schema_version > 0),
  aggregate_id TEXT NOT NULL REFERENCES aggregates(aggregate_id),
  aggregate_revision INTEGER NOT NULL CHECK (aggregate_revision > 0),
  kind TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  correlation_id TEXT NOT NULL,
  causation_id TEXT,
  command_id TEXT NOT NULL,
  actor_json TEXT NOT NULL CHECK (json_valid(actor_json)),
  data_json TEXT NOT NULL CHECK (json_valid(data_json)),
  metadata_json TEXT NOT NULL CHECK (json_valid(metadata_json)),
  UNIQUE(aggregate_id, aggregate_revision)
) STRICT;

CREATE INDEX events_correlation ON events(correlation_id, global_position);
CREATE INDEX events_kind_position ON events(kind, global_position);

CREATE TRIGGER events_reject_update
BEFORE UPDATE ON events
BEGIN
  SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TRIGGER events_reject_delete
BEFORE DELETE ON events
BEGIN
  SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TABLE commands (
  scope TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'completed')),
  response_status INTEGER,
  response_json TEXT CHECK (response_json IS NULL OR json_valid(response_json)),
  first_event_position INTEGER,
  last_event_position INTEGER,
  created_at TEXT NOT NULL,
  completed_at TEXT,
  CHECK (
    (status = 'pending' AND response_status IS NULL AND response_json IS NULL AND first_event_position IS NULL AND last_event_position IS NULL AND completed_at IS NULL) OR
    (status = 'completed' AND response_status IS NOT NULL AND response_json IS NOT NULL AND completed_at IS NOT NULL AND
      ((first_event_position IS NULL AND last_event_position IS NULL) OR
       (first_event_position IS NOT NULL AND last_event_position IS NOT NULL AND first_event_position <= last_event_position)))
  ),
  PRIMARY KEY(scope, idempotency_key)
) STRICT;

CREATE TABLE outbox (
  operation_id TEXT PRIMARY KEY,
  operation_kind TEXT NOT NULL,
  aggregate_id TEXT NOT NULL REFERENCES aggregates(aggregate_id),
  request_json TEXT NOT NULL CHECK (json_valid(request_json)),
  state TEXT NOT NULL CHECK (state IN ('prepared', 'leased', 'committed', 'reconcile_required')),
  available_at TEXT NOT NULL,
  lease_owner TEXT,
  lease_expires_at TEXT,
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  observation_json TEXT CHECK (observation_json IS NULL OR json_valid(observation_json)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (state = 'prepared' AND lease_owner IS NULL AND lease_expires_at IS NULL AND observation_json IS NULL) OR
    (state = 'leased' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL AND observation_json IS NULL) OR
    (state IN ('committed', 'reconcile_required') AND lease_owner IS NULL AND lease_expires_at IS NULL AND observation_json IS NOT NULL)
  )
) STRICT;

CREATE TABLE run_projection (
  run_id TEXT PRIMARY KEY,
  work_item_id TEXT NOT NULL,
  workflow_id TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'reconcile_required')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE approval_projection (
  approval_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  class TEXT NOT NULL CHECK (class IN ('workflow_checkpoint', 'workflow_control', 'provider_permission', 'external_delivery')),
  status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'changes_requested', 'rejected', 'denied', 'cancelled', 'expired')),
  scope_digest TEXT NOT NULL,
  policy_digest TEXT NOT NULL,
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE external_refs (
  owner_id TEXT NOT NULL REFERENCES aggregates(aggregate_id),
  adapter_key TEXT NOT NULL,
  ref_kind TEXT NOT NULL,
  opaque_ref_ciphertext BLOB NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(owner_id, adapter_key, ref_kind)
) STRICT;

CREATE TABLE projection_checkpoints (
  projection_name TEXT PRIMARY KEY,
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  reducer_version TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE lease_scopes (
  scope_kind TEXT NOT NULL CHECK (scope_kind IN ('attempt', 'repository', 'worktree')),
  scope_id TEXT NOT NULL,
  last_fencing_token INTEGER NOT NULL DEFAULT 0 CHECK (last_fencing_token >= 0),
  updated_at TEXT NOT NULL,
  PRIMARY KEY(scope_kind, scope_id)
) STRICT;

CREATE TABLE leases (
  lease_id TEXT PRIMARY KEY,
  scope_kind TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  holder_attempt_id TEXT NOT NULL,
  daemon_instance_id TEXT NOT NULL,
  fencing_token INTEGER NOT NULL CHECK (fencing_token > 0),
  acquired_at TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  host_boot_id TEXT NOT NULL,
  process_identity_json TEXT CHECK (process_identity_json IS NULL OR json_valid(process_identity_json)),
  state TEXT NOT NULL CHECK (state IN ('held', 'releasing', 'released', 'reconcile_required')),
  evidence_json TEXT CHECK (evidence_json IS NULL OR json_valid(evidence_json)),
  released_at TEXT,
  FOREIGN KEY(scope_kind, scope_id) REFERENCES lease_scopes(scope_kind, scope_id),
  UNIQUE(scope_kind, scope_id, fencing_token),
  CHECK (
    (state IN ('held', 'releasing') AND evidence_json IS NULL AND released_at IS NULL) OR
    (state = 'released' AND evidence_json IS NOT NULL AND released_at IS NOT NULL) OR
    (state = 'reconcile_required' AND evidence_json IS NOT NULL AND released_at IS NULL)
  )
) STRICT;

CREATE UNIQUE INDEX leases_one_active_scope
ON leases(scope_kind, scope_id)
WHERE state <> 'released';

CREATE INDEX leases_holder
ON leases(holder_attempt_id, state);

CREATE TABLE queue_entries (
  queue_kind TEXT NOT NULL CHECK (queue_kind IN ('attempt', 'repository_write')),
  scope_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  priority INTEGER NOT NULL CHECK (priority >= 0),
  available_at TEXT NOT NULL,
  enqueued_at TEXT NOT NULL,
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  PRIMARY KEY(queue_kind, scope_id, item_id)
) STRICT;

CREATE INDEX queue_entries_order
ON queue_entries(queue_kind, scope_id, priority DESC, enqueued_at, item_id);
