PRAGMA defer_foreign_keys = ON;

CREATE TABLE aggregates_v6 (
  aggregate_id TEXT PRIMARY KEY,
  aggregate_type TEXT NOT NULL CHECK (aggregate_type IN ('project','work','story','point','run','visit','attempt','artifact','approval','operation','assessment','input','permission')),
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
    (aggregate_type = 'input' AND aggregate_id GLOB 'input_*') OR
    (aggregate_type = 'permission' AND aggregate_id GLOB 'permission_*')
  )
) STRICT;
INSERT INTO aggregates_v6 SELECT * FROM aggregates;

CREATE TABLE events_v6 (
  global_position INTEGER PRIMARY KEY CHECK (global_position > 0), event_id TEXT NOT NULL UNIQUE,
  schema_version INTEGER NOT NULL CHECK (schema_version > 0), aggregate_id TEXT NOT NULL REFERENCES aggregates_v6(aggregate_id),
  aggregate_revision INTEGER NOT NULL CHECK (aggregate_revision > 0), kind TEXT NOT NULL, occurred_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL, correlation_id TEXT NOT NULL, causation_id TEXT, command_id TEXT NOT NULL,
  actor_json TEXT NOT NULL CHECK (json_valid(actor_json)), data_json TEXT NOT NULL CHECK (json_valid(data_json)),
  metadata_json TEXT NOT NULL CHECK (json_valid(metadata_json)), UNIQUE(aggregate_id, aggregate_revision)
) STRICT;
INSERT INTO events_v6 SELECT * FROM events;

CREATE TABLE outbox_v6 (
  operation_id TEXT PRIMARY KEY, operation_kind TEXT NOT NULL, aggregate_id TEXT NOT NULL REFERENCES aggregates_v6(aggregate_id),
  request_json TEXT NOT NULL CHECK (json_valid(request_json)), state TEXT NOT NULL CHECK (state IN ('prepared','leased','committed','reconcile_required')),
  available_at TEXT NOT NULL, lease_owner TEXT, lease_expires_at TEXT, attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  observation_json TEXT CHECK (observation_json IS NULL OR json_valid(observation_json)), created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK ((state='prepared' AND lease_owner IS NULL AND lease_expires_at IS NULL AND observation_json IS NULL) OR
    (state='leased' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL AND observation_json IS NULL) OR
    (state IN ('committed','reconcile_required') AND lease_owner IS NULL AND lease_expires_at IS NULL AND observation_json IS NOT NULL))
) STRICT;
INSERT INTO outbox_v6 SELECT * FROM outbox;

CREATE TABLE external_refs_v6 (
  owner_id TEXT NOT NULL REFERENCES aggregates_v6(aggregate_id), adapter_key TEXT NOT NULL, ref_kind TEXT NOT NULL,
  opaque_ref_ciphertext BLOB NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(owner_id, adapter_key, ref_kind)
) STRICT;
INSERT INTO external_refs_v6 SELECT * FROM external_refs;

CREATE TABLE run_workflow_snapshots_v6 (
  run_id TEXT PRIMARY KEY REFERENCES aggregates_v6(aggregate_id), workflow_name TEXT NOT NULL, workflow_version TEXT NOT NULL,
  workflow_digest TEXT NOT NULL CHECK (length(workflow_digest)=64 AND workflow_digest NOT GLOB '*[^0-9a-f]*'),
  workflow_json TEXT NOT NULL CHECK (json_valid(workflow_json) AND json_type(workflow_json)='object'),
  config_digest TEXT NOT NULL CHECK (length(config_digest)=64 AND config_digest NOT GLOB '*[^0-9a-f]*'),
  config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json) AND json_type(config_snapshot_json)='object'),
  created_at TEXT NOT NULL, FOREIGN KEY(workflow_name, workflow_version) REFERENCES workflow_versions(workflow_name, workflow_version)
) STRICT;
INSERT INTO run_workflow_snapshots_v6 SELECT * FROM run_workflow_snapshots;

DROP TABLE run_workflow_snapshots;
DROP TABLE external_refs;
DROP TABLE outbox;
DROP TABLE events;
DROP TABLE aggregates;
ALTER TABLE aggregates_v6 RENAME TO aggregates;
ALTER TABLE events_v6 RENAME TO events;
ALTER TABLE outbox_v6 RENAME TO outbox;
ALTER TABLE external_refs_v6 RENAME TO external_refs;
ALTER TABLE run_workflow_snapshots_v6 RENAME TO run_workflow_snapshots;
CREATE INDEX events_correlation ON events(correlation_id, global_position);
CREATE INDEX events_kind_position ON events(kind, global_position);
CREATE UNIQUE INDEX events_aggregate_command ON events(aggregate_id, command_id);
CREATE TRIGGER events_reject_update BEFORE UPDATE ON events BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
CREATE TRIGGER events_reject_delete BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
CREATE TRIGGER run_workflow_snapshots_reject_update BEFORE UPDATE ON run_workflow_snapshots BEGIN SELECT RAISE(ABORT, 'run workflow snapshots are immutable'); END;
CREATE TRIGGER run_workflow_snapshots_reject_delete BEFORE DELETE ON run_workflow_snapshots BEGIN SELECT RAISE(ABORT, 'run workflow snapshots are immutable'); END;

CREATE TABLE provider_permission_projection (
  permission_request_id TEXT PRIMARY KEY CHECK (permission_request_id GLOB 'permission_*'),
  run_id TEXT NOT NULL CHECK (run_id GLOB 'run_*'), attempt_id TEXT NOT NULL CHECK (attempt_id GLOB 'attempt_*'), node_id TEXT NOT NULL CHECK (node_id <> ''),
  provider_thread_id TEXT NOT NULL CHECK (provider_thread_id <> ''), provider_turn_id TEXT NOT NULL CHECK (provider_turn_id <> ''), provider_request_id TEXT NOT NULL CHECK (provider_request_id <> ''),
  interaction_kind TEXT NOT NULL CHECK (interaction_kind IN ('command','file','network','permission','tool')),
  scope_json TEXT NOT NULL CHECK (json_valid(scope_json) AND json_type(scope_json)='object'),
  scope_digest TEXT NOT NULL CHECK (length(scope_digest)=64 AND scope_digest NOT GLOB '*[^0-9a-f]*'),
  policy_digest TEXT NOT NULL CHECK (length(policy_digest)=64 AND policy_digest NOT GLOB '*[^0-9a-f]*'),
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND json_type(evidence_json)='object'),
  status TEXT NOT NULL CHECK (status IN ('pending','decision_recorded','responded')),
  decision TEXT CHECK (decision IS NULL OR decision IN ('allow_once','deny','cancel')),
  decision_action_key TEXT, decided_by_type TEXT CHECK (decided_by_type IS NULL OR decided_by_type IN ('user','external')),
  decided_by_id TEXT, decision_recorded_at TEXT, receipt_provider_request_id TEXT, delivered_at TEXT,
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1), last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(attempt_id, provider_request_id),
  CHECK (
    (status='pending' AND decision IS NULL AND decision_action_key IS NULL AND decided_by_type IS NULL AND decided_by_id IS NULL AND decision_recorded_at IS NULL AND receipt_provider_request_id IS NULL AND delivered_at IS NULL) OR
    (status='decision_recorded' AND decision IS NOT NULL AND decision_action_key IS NOT NULL AND decided_by_type IS NOT NULL AND decided_by_id IS NOT NULL AND decision_recorded_at IS NOT NULL AND receipt_provider_request_id IS NULL AND delivered_at IS NULL) OR
    (status='responded' AND decision IS NOT NULL AND decision_action_key IS NOT NULL AND decided_by_type IS NOT NULL AND decided_by_id IS NOT NULL AND decision_recorded_at IS NOT NULL AND receipt_provider_request_id=provider_request_id AND delivered_at IS NOT NULL)
  )
) STRICT;
CREATE INDEX provider_permission_projection_status ON provider_permission_projection(status, created_at, permission_request_id);
CREATE INDEX provider_permission_projection_attempt ON provider_permission_projection(attempt_id, created_at, permission_request_id);
