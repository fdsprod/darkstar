PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;

CREATE TABLE schema_migrations (
  version TEXT PRIMARY KEY,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL
) STRICT;

CREATE TABLE global_positions (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  last_position INTEGER NOT NULL CHECK (last_position >= 0)
) STRICT;

CREATE TABLE aggregates (
  aggregate_id TEXT PRIMARY KEY,
  aggregate_type TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE events (
  global_position INTEGER PRIMARY KEY,
  event_id TEXT NOT NULL UNIQUE,
  schema_version INTEGER NOT NULL,
  stream_id TEXT NOT NULL,
  stream_sequence INTEGER NOT NULL,
  aggregate_type TEXT NOT NULL,
  aggregate_id TEXT NOT NULL REFERENCES aggregates(aggregate_id),
  aggregate_revision INTEGER NOT NULL,
  kind TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  correlation_id TEXT NOT NULL,
  causation_id TEXT,
  command_id TEXT NOT NULL,
  actor_json TEXT NOT NULL CHECK (json_valid(actor_json)),
  data_json TEXT NOT NULL CHECK (json_valid(data_json)),
  metadata_json TEXT NOT NULL CHECK (json_valid(metadata_json)),
  UNIQUE(stream_id, stream_sequence),
  UNIQUE(aggregate_id, aggregate_revision)
) STRICT;

CREATE INDEX events_correlation ON events(correlation_id, global_position);
CREATE INDEX events_kind_position ON events(kind, global_position);

CREATE TABLE commands (
  scope TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  status TEXT NOT NULL,
  response_status INTEGER,
  response_json TEXT CHECK (response_json IS NULL OR json_valid(response_json)),
  first_event_position INTEGER,
  last_event_position INTEGER,
  created_at TEXT NOT NULL,
  completed_at TEXT,
  PRIMARY KEY(scope, idempotency_key)
) STRICT;

CREATE TABLE outbox (
  operation_id TEXT PRIMARY KEY,
  operation_kind TEXT NOT NULL,
  aggregate_id TEXT NOT NULL REFERENCES aggregates(aggregate_id),
  request_json TEXT NOT NULL CHECK (json_valid(request_json)),
  state TEXT NOT NULL,
  available_at TEXT NOT NULL,
  lease_owner TEXT,
  lease_expires_at TEXT,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  observation_json TEXT CHECK (observation_json IS NULL OR json_valid(observation_json)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_projection (
  run_id TEXT PRIMARY KEY,
  work_item_id TEXT NOT NULL,
  workflow_id TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  status TEXT NOT NULL,
  resource_version INTEGER NOT NULL,
  last_global_position INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE approval_projection (
  approval_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  class TEXT NOT NULL,
  status TEXT NOT NULL,
  scope_digest TEXT NOT NULL,
  policy_digest TEXT NOT NULL,
  resource_version INTEGER NOT NULL,
  last_global_position INTEGER NOT NULL,
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
  last_global_position INTEGER NOT NULL,
  reducer_version TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;
