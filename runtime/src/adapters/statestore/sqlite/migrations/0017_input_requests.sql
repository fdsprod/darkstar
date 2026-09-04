PRAGMA defer_foreign_keys = ON;

CREATE TABLE aggregates_v5 (
  aggregate_id TEXT PRIMARY KEY,
  aggregate_type TEXT NOT NULL CHECK (aggregate_type IN ('project', 'work', 'story', 'point', 'run', 'visit', 'attempt', 'artifact', 'approval', 'operation', 'assessment', 'input')),
  revision INTEGER NOT NULL CHECK (revision >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (aggregate_type = 'project' AND aggregate_id GLOB 'project_*') OR
    (aggregate_type = 'work' AND aggregate_id GLOB 'work_*') OR
    (aggregate_type = 'story' AND aggregate_id GLOB 'story_*') OR
    (aggregate_type = 'point' AND aggregate_id GLOB 'point_*') OR
    (aggregate_type = 'run' AND aggregate_id GLOB 'run_*') OR
    (aggregate_type = 'visit' AND aggregate_id GLOB 'visit_*') OR
    (aggregate_type = 'attempt' AND aggregate_id GLOB 'attempt_*') OR
    (aggregate_type = 'artifact' AND aggregate_id GLOB 'artifact_*') OR
    (aggregate_type = 'approval' AND aggregate_id GLOB 'approval_*') OR
    (aggregate_type = 'operation' AND aggregate_id GLOB 'operation_*') OR
    (aggregate_type = 'assessment' AND aggregate_id GLOB 'assessment_*') OR
    (aggregate_type = 'input' AND aggregate_id GLOB 'input_*')
  )
) STRICT;

INSERT INTO aggregates_v5 SELECT * FROM aggregates;

CREATE TABLE events_v5 (
  global_position INTEGER PRIMARY KEY CHECK (global_position > 0),
  event_id TEXT NOT NULL UNIQUE,
  schema_version INTEGER NOT NULL CHECK (schema_version > 0),
  aggregate_id TEXT NOT NULL REFERENCES aggregates_v5(aggregate_id),
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
INSERT INTO events_v5 SELECT * FROM events;

CREATE TABLE outbox_v5 (
  operation_id TEXT PRIMARY KEY,
  operation_kind TEXT NOT NULL,
  aggregate_id TEXT NOT NULL REFERENCES aggregates_v5(aggregate_id),
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
INSERT INTO outbox_v5 SELECT * FROM outbox;

CREATE TABLE external_refs_v5 (
  owner_id TEXT NOT NULL REFERENCES aggregates_v5(aggregate_id),
  adapter_key TEXT NOT NULL,
  ref_kind TEXT NOT NULL,
  opaque_ref_ciphertext BLOB NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(owner_id, adapter_key, ref_kind)
) STRICT;
INSERT INTO external_refs_v5 SELECT * FROM external_refs;

CREATE TABLE run_workflow_snapshots_v5 (
  run_id TEXT PRIMARY KEY REFERENCES aggregates_v5(aggregate_id),
  workflow_name TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  workflow_digest TEXT NOT NULL CHECK (length(workflow_digest) = 64 AND workflow_digest NOT GLOB '*[^0-9a-f]*'),
  workflow_json TEXT NOT NULL CHECK (json_valid(workflow_json) AND json_type(workflow_json) = 'object'),
  config_digest TEXT NOT NULL CHECK (length(config_digest) = 64 AND config_digest NOT GLOB '*[^0-9a-f]*'),
  config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json) AND json_type(config_snapshot_json) = 'object'),
  created_at TEXT NOT NULL,
  FOREIGN KEY(workflow_name, workflow_version) REFERENCES workflow_versions(workflow_name, workflow_version)
) STRICT;
INSERT INTO run_workflow_snapshots_v5 SELECT * FROM run_workflow_snapshots;

DROP TABLE run_workflow_snapshots;
DROP TABLE external_refs;
DROP TABLE outbox;
DROP TABLE events;
DROP TABLE aggregates;

ALTER TABLE aggregates_v5 RENAME TO aggregates;
ALTER TABLE events_v5 RENAME TO events;
ALTER TABLE outbox_v5 RENAME TO outbox;
ALTER TABLE external_refs_v5 RENAME TO external_refs;
ALTER TABLE run_workflow_snapshots_v5 RENAME TO run_workflow_snapshots;

CREATE INDEX events_correlation ON events(correlation_id, global_position);
CREATE INDEX events_kind_position ON events(kind, global_position);
CREATE UNIQUE INDEX events_aggregate_command ON events(aggregate_id, command_id);

CREATE TRIGGER events_reject_update BEFORE UPDATE ON events BEGIN
  SELECT RAISE(ABORT, 'events are append-only');
END;
CREATE TRIGGER events_reject_delete BEFORE DELETE ON events BEGIN
  SELECT RAISE(ABORT, 'events are append-only');
END;
CREATE TRIGGER run_workflow_snapshots_reject_update BEFORE UPDATE ON run_workflow_snapshots BEGIN
  SELECT RAISE(ABORT, 'run workflow snapshots are immutable');
END;
CREATE TRIGGER run_workflow_snapshots_reject_delete BEFORE DELETE ON run_workflow_snapshots BEGIN
  SELECT RAISE(ABORT, 'run workflow snapshots are immutable');
END;

CREATE TABLE input_request_projection (
  input_request_id TEXT PRIMARY KEY CHECK (input_request_id GLOB 'input_*'),
  run_id TEXT NOT NULL CHECK (run_id GLOB 'run_*'),
  attempt_id TEXT NOT NULL CHECK (attempt_id GLOB 'attempt_*'),
  node_id TEXT NOT NULL CHECK (node_id <> ''),
  provider_thread_id TEXT NOT NULL CHECK (provider_thread_id <> ''),
  provider_request_id TEXT NOT NULL CHECK (provider_request_id <> ''),
  scope_digest TEXT NOT NULL CHECK (length(scope_digest) = 64 AND scope_digest NOT GLOB '*[^0-9a-f]*'),
  request_json TEXT NOT NULL CHECK (json_valid(request_json) AND json_type(request_json) = 'object'),
  status TEXT NOT NULL CHECK (status IN ('pending', 'answer_recorded', 'answered')),
  answer_json TEXT CHECK (answer_json IS NULL OR json_valid(answer_json)),
  answer_action_key TEXT,
  answered_by_type TEXT CHECK (answered_by_type IS NULL OR answered_by_type IN ('user', 'external')),
  answered_by_id TEXT,
  answer_recorded_at TEXT,
  receipt_provider_request_id TEXT,
  delivered_at TEXT,
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(attempt_id, provider_request_id),
  CHECK (
    (status = 'pending' AND answer_json IS NULL AND answer_action_key IS NULL AND answered_by_type IS NULL AND answered_by_id IS NULL AND answer_recorded_at IS NULL AND receipt_provider_request_id IS NULL AND delivered_at IS NULL) OR
    (status = 'answer_recorded' AND answer_json IS NOT NULL AND answer_action_key IS NOT NULL AND answered_by_type IS NOT NULL AND answered_by_id IS NOT NULL AND answer_recorded_at IS NOT NULL AND receipt_provider_request_id IS NULL AND delivered_at IS NULL) OR
    (status = 'answered' AND answer_json IS NOT NULL AND answer_action_key IS NOT NULL AND answered_by_type IS NOT NULL AND answered_by_id IS NOT NULL AND answer_recorded_at IS NOT NULL AND receipt_provider_request_id = provider_request_id AND delivered_at IS NOT NULL)
  )
) STRICT;

CREATE INDEX input_request_projection_run ON input_request_projection(run_id, created_at, input_request_id);
CREATE INDEX input_request_projection_attempt ON input_request_projection(attempt_id, created_at, input_request_id);
