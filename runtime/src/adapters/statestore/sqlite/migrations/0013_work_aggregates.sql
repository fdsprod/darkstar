PRAGMA defer_foreign_keys = ON;

CREATE TABLE aggregates_v3 (
  aggregate_id TEXT PRIMARY KEY,
  aggregate_type TEXT NOT NULL CHECK (aggregate_type IN ('project', 'work', 'story', 'point', 'run', 'visit', 'attempt', 'artifact', 'approval', 'operation')),
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
    (aggregate_type = 'operation' AND aggregate_id GLOB 'operation_*')
  )
) STRICT;

INSERT INTO aggregates_v3 SELECT * FROM aggregates;

CREATE TABLE events_v3 (
  global_position INTEGER PRIMARY KEY CHECK (global_position > 0),
  event_id TEXT NOT NULL UNIQUE,
  schema_version INTEGER NOT NULL CHECK (schema_version > 0),
  aggregate_id TEXT NOT NULL REFERENCES aggregates_v3(aggregate_id),
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

INSERT INTO events_v3 SELECT * FROM events;

CREATE TABLE outbox_v3 (
  operation_id TEXT PRIMARY KEY,
  operation_kind TEXT NOT NULL,
  aggregate_id TEXT NOT NULL REFERENCES aggregates_v3(aggregate_id),
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

INSERT INTO outbox_v3 SELECT * FROM outbox;

CREATE TABLE external_refs_v3 (
  owner_id TEXT NOT NULL REFERENCES aggregates_v3(aggregate_id),
  adapter_key TEXT NOT NULL,
  ref_kind TEXT NOT NULL,
  opaque_ref_ciphertext BLOB NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(owner_id, adapter_key, ref_kind)
) STRICT;

INSERT INTO external_refs_v3 SELECT * FROM external_refs;

CREATE TABLE run_projection_v3 (
  run_id TEXT PRIMARY KEY,
  work_item_id TEXT NOT NULL,
  workflow_id TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  workflow_digest TEXT NOT NULL DEFAULT '',
  route_digest TEXT NOT NULL DEFAULT '',
  route_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(route_snapshot_json) AND json_type(route_snapshot_json) = 'object'),
  priority INTEGER NOT NULL DEFAULT 0 CHECK (priority >= 0),
  status TEXT NOT NULL CHECK (status IN ('draft', 'ready', 'queued', 'running', 'waiting', 'blocked', 'completed', 'failed', 'cancelled', 'reconcile_required')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (workflow_digest = '' AND route_digest = '' AND route_snapshot_json = '{}') OR
    (length(workflow_digest) = 64 AND workflow_digest NOT GLOB '*[^0-9a-f]*' AND
     length(route_digest) = 64 AND route_digest NOT GLOB '*[^0-9a-f]*' AND route_snapshot_json <> '{}')
  )
) STRICT;

INSERT INTO run_projection_v3(run_id, work_item_id, workflow_id, workflow_version, status,
  resource_version, last_global_position, created_at, updated_at)
SELECT run_id, work_item_id, workflow_id, workflow_version, status,
  resource_version, last_global_position, created_at, updated_at FROM run_projection;

CREATE TABLE project_projection (
  project_id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK (name <> ''),
  source_hash TEXT NOT NULL CHECK (length(source_hash) = 64 AND source_hash NOT GLOB '*[^0-9a-f]*'),
  status TEXT NOT NULL CHECK (status IN ('active', 'archived')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE work_item_projection (
  work_item_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES project_projection(project_id),
  title TEXT NOT NULL CHECK (title <> ''),
  source_hash TEXT NOT NULL CHECK (length(source_hash) = 64 AND source_hash NOT GLOB '*[^0-9a-f]*'),
  priority INTEGER NOT NULL CHECK (priority >= 0),
  status TEXT NOT NULL CHECK (status IN ('open', 'active', 'completed', 'cancelled')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX work_item_projection_project ON work_item_projection(project_id, priority DESC, created_at, work_item_id);

CREATE TABLE story_projection (
  story_id TEXT PRIMARY KEY,
  work_item_id TEXT NOT NULL REFERENCES work_item_projection(work_item_id),
  title TEXT NOT NULL CHECK (title <> ''),
  source_hash TEXT NOT NULL CHECK (length(source_hash) = 64 AND source_hash NOT GLOB '*[^0-9a-f]*'),
  priority INTEGER NOT NULL CHECK (priority >= 0),
  position INTEGER NOT NULL CHECK (position >= 0),
  status TEXT NOT NULL CHECK (status IN ('planned', 'ready', 'running', 'completed', 'cancelled', 'retired')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX story_projection_work_item ON story_projection(work_item_id, position, priority DESC, story_id);

CREATE TABLE point_projection (
  point_id TEXT PRIMARY KEY,
  story_id TEXT NOT NULL REFERENCES story_projection(story_id),
  revision INTEGER NOT NULL CHECK (revision >= 1),
  title TEXT NOT NULL CHECK (title <> ''),
  source_hash TEXT NOT NULL CHECK (length(source_hash) = 64 AND source_hash NOT GLOB '*[^0-9a-f]*'),
  priority INTEGER NOT NULL CHECK (priority >= 0),
  position INTEGER NOT NULL CHECK (position >= 0),
  status TEXT NOT NULL CHECK (status IN ('planned', 'ready', 'running', 'validating', 'awaiting_approval', 'accepted', 'committed', 'published', 'failed', 'rejected', 'superseded', 'reconcile_required')),
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX point_projection_story ON point_projection(story_id, position, priority DESC, point_id);

CREATE TABLE point_dependencies (
  point_id TEXT NOT NULL REFERENCES point_projection(point_id),
  depends_on_point_id TEXT NOT NULL REFERENCES point_projection(point_id),
  source_revision INTEGER NOT NULL CHECK (source_revision >= 1),
  PRIMARY KEY(point_id, depends_on_point_id),
  CHECK (point_id <> depends_on_point_id)
) STRICT;

ALTER TABLE attempt_projection ADD COLUMN point_id TEXT REFERENCES point_projection(point_id);
ALTER TABLE attempt_projection ADD COLUMN point_revision INTEGER;
ALTER TABLE attempt_projection ADD COLUMN priority INTEGER NOT NULL DEFAULT 0 CHECK (priority >= 0);

DROP TABLE external_refs;
DROP TABLE outbox;
DROP TABLE events;
DROP TABLE run_projection;
DROP TABLE aggregates;

ALTER TABLE aggregates_v3 RENAME TO aggregates;
ALTER TABLE events_v3 RENAME TO events;
ALTER TABLE outbox_v3 RENAME TO outbox;
ALTER TABLE external_refs_v3 RENAME TO external_refs;
ALTER TABLE run_projection_v3 RENAME TO run_projection;

CREATE INDEX events_correlation ON events(correlation_id, global_position);
CREATE INDEX events_kind_position ON events(kind, global_position);
CREATE UNIQUE INDEX events_aggregate_command ON events(aggregate_id, command_id);
CREATE INDEX attempt_projection_point ON attempt_projection(point_id, point_revision, created_at, attempt_id);

CREATE TRIGGER attempt_projection_validate_owner_insert
BEFORE INSERT ON attempt_projection
WHEN NOT (
  (NEW.point_id IS NULL AND NEW.point_revision IS NULL AND NEW.node_id <> '') OR
  (NEW.point_id IS NOT NULL AND NEW.point_revision IS NOT NULL AND NEW.point_revision >= 1 AND NEW.visit_id = '' AND NEW.node_id = '')
)
BEGIN
  SELECT RAISE(ABORT, 'attempt must belong to exactly one visit or point revision');
END;

CREATE TRIGGER attempt_projection_validate_owner_update
BEFORE UPDATE ON attempt_projection
WHEN NOT (
  (NEW.point_id IS NULL AND NEW.point_revision IS NULL AND NEW.node_id <> '') OR
  (NEW.point_id IS NOT NULL AND NEW.point_revision IS NOT NULL AND NEW.point_revision >= 1 AND NEW.visit_id = '' AND NEW.node_id = '')
)
BEGIN
  SELECT RAISE(ABORT, 'attempt must belong to exactly one visit or point revision');
END;

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
