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
CREATE UNIQUE INDEX events_aggregate_command ON events(aggregate_id, command_id);

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

CREATE TABLE attempt_projection (
  attempt_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  visit_id TEXT NOT NULL DEFAULT '',
  node_id TEXT NOT NULL DEFAULT '',
  point_id TEXT REFERENCES point_projection(point_id),
  point_revision INTEGER,
  priority INTEGER NOT NULL DEFAULT 0 CHECK (priority >= 0),
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
    (point_id IS NULL AND point_revision IS NULL AND node_id <> '') OR
    (point_id IS NOT NULL AND point_revision IS NOT NULL AND point_revision >= 1 AND visit_id = '' AND node_id = '')
  ),
  CHECK (
    (provider_thread_id = '' AND provider_turn_id = '' AND process_owner_id = '' AND last_sequence = 0) OR
    (provider_thread_id <> '' AND provider_turn_id <> '' AND process_owner_id <> '')
  ),
  CHECK (status NOT IN ('running', 'validating', 'succeeded') OR provider_thread_id <> '')
) STRICT;

CREATE INDEX attempt_projection_run ON attempt_projection(run_id, created_at, attempt_id);
CREATE INDEX attempt_projection_point ON attempt_projection(point_id, point_revision, created_at, attempt_id);
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

CREATE TABLE approval_projection (
  approval_id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  class TEXT NOT NULL CHECK (class IN ('workflow_checkpoint', 'workflow_control', 'provider_permission', 'external_delivery')),
  status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'changes_requested', 'rejected', 'denied', 'cancelled', 'expired')),
  checkpoint_id TEXT NOT NULL DEFAULT '',
  visit_id TEXT NOT NULL DEFAULT '',
  node_id TEXT NOT NULL DEFAULT '',
  attempt_id TEXT NOT NULL DEFAULT '',
  checkpoint_revision INTEGER NOT NULL DEFAULT 0 CHECK (checkpoint_revision >= 0),
  candidate_artifact_id TEXT NOT NULL DEFAULT '',
  candidate_artifact_version INTEGER NOT NULL DEFAULT 0 CHECK (candidate_artifact_version >= 0),
  candidate_digest TEXT NOT NULL DEFAULT '',
  checkpoint_mode TEXT NOT NULL DEFAULT '',
  max_revisions INTEGER CHECK (max_revisions IS NULL OR max_revisions > 0),
  scope_digest TEXT NOT NULL,
  policy_digest TEXT NOT NULL,
  decision_action TEXT,
  decision_action_key TEXT,
  decision_comment TEXT,
  decided_by_type TEXT CHECK (decided_by_type IS NULL OR decided_by_type IN ('user', 'system', 'provider', 'external')),
  decided_by_id TEXT,
  decided_at TEXT,
  resource_version INTEGER NOT NULL CHECK (resource_version >= 1),
  last_global_position INTEGER NOT NULL CHECK (last_global_position >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (checkpoint_id = '' AND visit_id = '' AND node_id = '' AND attempt_id = '' AND checkpoint_revision = 0 AND
      candidate_artifact_id = '' AND candidate_artifact_version = 0 AND candidate_digest = '' AND checkpoint_mode = '' AND max_revisions IS NULL) OR
    (class = 'workflow_checkpoint' AND checkpoint_id GLOB 'checkpoint_*' AND visit_id GLOB 'visit_*' AND node_id <> '' AND
      attempt_id GLOB 'attempt_*' AND checkpoint_revision > 0 AND candidate_artifact_id GLOB 'artifact_*' AND
      candidate_artifact_version > 0 AND length(candidate_digest) = 64 AND candidate_digest NOT GLOB '*[^0-9a-f]*' AND
      checkpoint_mode IN ('approve', 'approve_on_change'))
  ),
  CHECK (
    (decision_action IS NULL AND decision_action_key IS NULL AND decision_comment IS NULL AND decided_by_type IS NULL AND decided_by_id IS NULL AND decided_at IS NULL) OR
    (decision_action IS NOT NULL AND decision_action_key IS NOT NULL AND decision_comment IS NOT NULL AND decided_by_type IS NOT NULL AND decided_by_id IS NOT NULL AND decided_at IS NOT NULL)
  ),
  CHECK (status <> 'pending' OR decision_action IS NULL)
) STRICT;

CREATE UNIQUE INDEX approval_projection_checkpoint_revision
ON approval_projection(checkpoint_id, checkpoint_revision)
WHERE checkpoint_id <> '';

CREATE INDEX approval_projection_run
ON approval_projection(run_id, created_at, approval_id);

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

CREATE TABLE recovery_decisions (
  startup_id TEXT NOT NULL,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('lease', 'operation')),
  subject_id TEXT NOT NULL,
  subject_authority TEXT NOT NULL,
  subject_state TEXT NOT NULL,
  subject_payload_json TEXT NOT NULL CHECK (json_valid(subject_payload_json) AND json_type(subject_payload_json) = 'object'),
  outcome TEXT NOT NULL CHECK (outcome IN ('adopt', 'resume', 'retry', 'interrupt', 'reconcile_required')),
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND json_type(evidence_json) = 'object'),
  recorded_at TEXT NOT NULL,
  PRIMARY KEY(startup_id, subject_kind, subject_id)
) STRICT;

CREATE INDEX recovery_decisions_subject
ON recovery_decisions(subject_kind, subject_id, recorded_at);

CREATE TRIGGER recovery_decisions_reject_update
BEFORE UPDATE ON recovery_decisions
BEGIN
  SELECT RAISE(ABORT, 'recovery decisions are append-only');
END;

CREATE TRIGGER recovery_decisions_reject_delete
BEFORE DELETE ON recovery_decisions
BEGIN
  SELECT RAISE(ABORT, 'recovery decisions are append-only');
END;

CREATE TABLE workflow_versions (
  workflow_name TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  workflow_digest TEXT NOT NULL CHECK (length(workflow_digest) = 64 AND workflow_digest NOT GLOB '*[^0-9a-f]*'),
  document_json TEXT NOT NULL CHECK (json_valid(document_json) AND json_type(document_json) = 'object'),
  source_scope TEXT NOT NULL CHECK (source_scope IN ('default', 'user', 'project')),
  source_reference TEXT NOT NULL CHECK (source_reference <> ''),
  installed_at TEXT NOT NULL,
  PRIMARY KEY(workflow_name, workflow_version)
) STRICT;

CREATE TRIGGER workflow_versions_reject_update
BEFORE UPDATE ON workflow_versions
BEGIN
  SELECT RAISE(ABORT, 'workflow versions are immutable');
END;

CREATE TRIGGER workflow_versions_reject_delete
BEFORE DELETE ON workflow_versions
BEGIN
  SELECT RAISE(ABORT, 'workflow versions are immutable');
END;

CREATE TABLE run_workflow_snapshots (
  run_id TEXT PRIMARY KEY REFERENCES aggregates(aggregate_id),
  workflow_name TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  workflow_digest TEXT NOT NULL CHECK (length(workflow_digest) = 64 AND workflow_digest NOT GLOB '*[^0-9a-f]*'),
  workflow_json TEXT NOT NULL CHECK (json_valid(workflow_json) AND json_type(workflow_json) = 'object'),
  config_digest TEXT NOT NULL CHECK (length(config_digest) = 64 AND config_digest NOT GLOB '*[^0-9a-f]*'),
  config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json) AND json_type(config_snapshot_json) = 'object'),
  created_at TEXT NOT NULL,
  FOREIGN KEY(workflow_name, workflow_version) REFERENCES workflow_versions(workflow_name, workflow_version)
) STRICT;

CREATE TRIGGER run_workflow_snapshots_reject_update
BEFORE UPDATE ON run_workflow_snapshots
BEGIN
  SELECT RAISE(ABORT, 'run workflow snapshots are immutable');
END;

CREATE TRIGGER run_workflow_snapshots_reject_delete
BEFORE DELETE ON run_workflow_snapshots
BEGIN
  SELECT RAISE(ABORT, 'run workflow snapshots are immutable');
END;

CREATE TABLE artifact_versions (
  artifact_id TEXT NOT NULL CHECK (artifact_id GLOB 'artifact_*'),
  version INTEGER NOT NULL CHECK (version > 0),
  idempotency_key TEXT NOT NULL CHECK (idempotency_key <> ''),
  source_kind TEXT NOT NULL CHECK (source_kind IN ('file', 'paste', 'stdin', 'generated', 'external')),
  source_name TEXT NOT NULL,
  blob_digest TEXT NOT NULL CHECK (length(blob_digest) = 64 AND blob_digest NOT GLOB '*[^0-9a-f]*'),
  size INTEGER NOT NULL CHECK (size >= 0),
  declared_media_type TEXT NOT NULL CHECK (declared_media_type <> ''),
  detected_media_type TEXT NOT NULL CHECK (detected_media_type <> ''),
  locator TEXT NOT NULL CHECK (locator <> ''),
  sensitivity TEXT NOT NULL CHECK (sensitivity IN ('unknown', 'public', 'internal', 'sensitive', 'secret')),
  creator TEXT NOT NULL CHECK (creator <> ''),
  status TEXT NOT NULL CHECK (status IN ('stored', 'stored_uninspectable', 'quarantined')),
  producer_name TEXT NOT NULL CHECK (producer_name <> ''),
  producer_version TEXT NOT NULL CHECK (producer_version <> ''),
  roles_json TEXT NOT NULL CHECK (json_valid(roles_json) AND json_type(roles_json) = 'array'),
  tags_json TEXT NOT NULL CHECK (json_valid(tags_json) AND json_type(tags_json) = 'array'),
  metadata_json TEXT NOT NULL CHECK (json_valid(metadata_json) AND json_type(metadata_json) = 'object'),
  origin_kind TEXT NOT NULL CHECK (origin_kind IN ('attempt', 'operation')),
  operation_id TEXT NOT NULL CHECK (operation_id <> ''),
  run_id TEXT,
  node_id TEXT,
  attempt_id TEXT,
  source_artifact_id TEXT,
  source_artifact_version INTEGER,
  created_at TEXT NOT NULL,
  PRIMARY KEY(artifact_id, version),
  UNIQUE(artifact_id, idempotency_key),
  FOREIGN KEY(source_artifact_id, source_artifact_version) REFERENCES artifact_versions(artifact_id, version),
  CHECK (
    (origin_kind = 'attempt' AND run_id IS NOT NULL AND node_id IS NOT NULL AND attempt_id IS NOT NULL) OR
    (origin_kind = 'operation' AND run_id IS NULL AND node_id IS NULL AND attempt_id IS NULL)
  ),
  CHECK (
    (source_artifact_id IS NULL AND source_artifact_version IS NULL) OR
    (source_artifact_id IS NOT NULL AND source_artifact_version IS NOT NULL AND source_artifact_version > 0)
  )
) STRICT;

CREATE INDEX artifact_versions_digest
ON artifact_versions(blob_digest, artifact_id, version);

CREATE TRIGGER artifact_versions_reject_update
BEFORE UPDATE ON artifact_versions
BEGIN
  SELECT RAISE(ABORT, 'artifact versions are immutable');
END;

CREATE TRIGGER artifact_versions_reject_delete
BEFORE DELETE ON artifact_versions
BEGIN
  SELECT RAISE(ABORT, 'artifact versions are immutable');
END;

CREATE TABLE artifact_dependencies (
  source_artifact_id TEXT NOT NULL,
  source_artifact_version INTEGER NOT NULL CHECK (source_artifact_version > 0),
  dependent_artifact_id TEXT NOT NULL,
  dependent_artifact_version INTEGER NOT NULL CHECK (dependent_artifact_version > 0),
  impact TEXT NOT NULL CHECK (impact IN ('potentially_stale', 'invalidated')),
  created_at TEXT NOT NULL,
  PRIMARY KEY(source_artifact_id, source_artifact_version, dependent_artifact_id, dependent_artifact_version),
  FOREIGN KEY(source_artifact_id, source_artifact_version) REFERENCES artifact_versions(artifact_id, version),
  FOREIGN KEY(dependent_artifact_id, dependent_artifact_version) REFERENCES artifact_versions(artifact_id, version),
  CHECK (source_artifact_id <> dependent_artifact_id OR source_artifact_version <> dependent_artifact_version)
) STRICT;

CREATE INDEX artifact_dependencies_dependent
ON artifact_dependencies(dependent_artifact_id, dependent_artifact_version, source_artifact_id, source_artifact_version);

CREATE TRIGGER artifact_dependencies_reject_update
BEFORE UPDATE ON artifact_dependencies
BEGIN
  SELECT RAISE(ABORT, 'artifact dependencies are immutable');
END;

CREATE TRIGGER artifact_dependencies_reject_delete
BEFORE DELETE ON artifact_dependencies
BEGIN
  SELECT RAISE(ABORT, 'artifact dependencies are immutable');
END;

CREATE TABLE artifact_invalidations (
  trigger_artifact_id TEXT NOT NULL,
  trigger_artifact_version INTEGER NOT NULL CHECK (trigger_artifact_version > 1),
  descendant_artifact_id TEXT NOT NULL,
  descendant_artifact_version INTEGER NOT NULL CHECK (descendant_artifact_version > 0),
  freshness TEXT NOT NULL CHECK (freshness IN ('potentially_stale', 'invalidated')),
  created_at TEXT NOT NULL,
  PRIMARY KEY(trigger_artifact_id, trigger_artifact_version, descendant_artifact_id, descendant_artifact_version),
  FOREIGN KEY(trigger_artifact_id, trigger_artifact_version) REFERENCES artifact_versions(artifact_id, version),
  FOREIGN KEY(descendant_artifact_id, descendant_artifact_version) REFERENCES artifact_versions(artifact_id, version)
) STRICT;

CREATE INDEX artifact_invalidations_descendant
ON artifact_invalidations(descendant_artifact_id, descendant_artifact_version, trigger_artifact_id, trigger_artifact_version);

CREATE TRIGGER artifact_invalidations_reject_update
BEFORE UPDATE ON artifact_invalidations
BEGIN
  SELECT RAISE(ABORT, 'artifact invalidations are append-only');
END;

CREATE TRIGGER artifact_invalidations_reject_delete
BEFORE DELETE ON artifact_invalidations
BEGIN
  SELECT RAISE(ABORT, 'artifact invalidations are append-only');
END;

CREATE TABLE artifact_binding_versions (
  binding_id TEXT NOT NULL CHECK (binding_id GLOB 'binding_*'),
  version INTEGER NOT NULL CHECK (version > 0),
  idempotency_key TEXT NOT NULL CHECK (idempotency_key <> ''),
  state TEXT NOT NULL CHECK (state IN ('bound', 'unbound')),
  artifact_id TEXT NOT NULL,
  artifact_version INTEGER NOT NULL CHECK (artifact_version > 0),
  target_kind TEXT NOT NULL CHECK (target_kind IN ('project', 'work', 'run', 'node', 'checkpoint', 'decision', 'story', 'implementation_point')),
  target_id TEXT NOT NULL CHECK (target_id <> ''),
  created_at TEXT NOT NULL,
  PRIMARY KEY(binding_id, version),
  UNIQUE(binding_id, idempotency_key),
  FOREIGN KEY(artifact_id, artifact_version) REFERENCES artifact_versions(artifact_id, version)
) STRICT;

CREATE INDEX artifact_binding_versions_target
ON artifact_binding_versions(target_kind, target_id, binding_id, version);

CREATE TRIGGER artifact_binding_versions_reject_update
BEFORE UPDATE ON artifact_binding_versions
BEGIN
  SELECT RAISE(ABORT, 'artifact binding versions are immutable');
END;

CREATE TRIGGER artifact_binding_versions_reject_delete
BEFORE DELETE ON artifact_binding_versions
BEGIN
  SELECT RAISE(ABORT, 'artifact binding versions are immutable');
END;

CREATE TABLE artifact_representations (
  representation_id TEXT PRIMARY KEY CHECK (representation_id GLOB 'representation_*'),
  idempotency_key TEXT NOT NULL CHECK (idempotency_key <> ''),
  artifact_id TEXT NOT NULL,
  artifact_version INTEGER NOT NULL CHECK (artifact_version > 0),
  representation_kind TEXT NOT NULL CHECK (representation_kind IN ('text', 'structured', 'table', 'image', 'preview', 'descriptor')),
  processor_name TEXT NOT NULL CHECK (processor_name <> ''),
  processor_version TEXT NOT NULL CHECK (processor_version <> ''),
  media_type TEXT NOT NULL CHECK (media_type <> ''),
  locator TEXT NOT NULL CHECK (locator <> ''),
  digest TEXT NOT NULL CHECK (length(digest) = 64 AND digest NOT GLOB '*[^0-9a-f]*'),
  size INTEGER NOT NULL CHECK (size >= 0),
  token_estimate INTEGER NOT NULL CHECK (token_estimate >= 0),
  truncated INTEGER NOT NULL CHECK (truncated IN (0, 1)),
  disclosure TEXT NOT NULL CHECK (disclosure IN ('raw', 'redacted', 'withheld')),
  diagnostics_json TEXT NOT NULL CHECK (json_valid(diagnostics_json) AND json_type(diagnostics_json) = 'array'),
  metadata_json TEXT NOT NULL CHECK (json_valid(metadata_json) AND json_type(metadata_json) = 'object'),
  created_at TEXT NOT NULL,
  UNIQUE(artifact_id, artifact_version, idempotency_key, representation_kind),
  FOREIGN KEY(artifact_id, artifact_version) REFERENCES artifact_versions(artifact_id, version)
) STRICT;

CREATE INDEX artifact_representations_artifact
ON artifact_representations(artifact_id, artifact_version, representation_kind, representation_id);

CREATE TRIGGER artifact_representations_reject_update
BEFORE UPDATE ON artifact_representations
BEGIN
  SELECT RAISE(ABORT, 'artifact representations are immutable');
END;

CREATE TRIGGER artifact_representations_reject_delete
BEFORE DELETE ON artifact_representations
BEGIN
  SELECT RAISE(ABORT, 'artifact representations are immutable');
END;

CREATE TABLE context_manifests (
  manifest_id TEXT PRIMARY KEY CHECK (manifest_id GLOB 'manifest_*'),
  idempotency_key TEXT NOT NULL UNIQUE CHECK (idempotency_key <> ''),
  run_id TEXT NOT NULL CHECK (run_id <> ''),
  node_id TEXT NOT NULL CHECK (node_id <> ''),
  attempt_id TEXT NOT NULL UNIQUE REFERENCES attempt_projection(attempt_id),
  policy_version TEXT NOT NULL CHECK (policy_version <> ''),
  budget INTEGER NOT NULL CHECK (budget >= 0),
  reserved_tokens INTEGER NOT NULL CHECK (reserved_tokens >= 0 AND reserved_tokens <= budget),
  entries_json TEXT NOT NULL CHECK (json_valid(entries_json) AND json_type(entries_json) = 'array'),
  omissions_json TEXT NOT NULL CHECK (json_valid(omissions_json) AND json_type(omissions_json) = 'array'),
  instructions_json TEXT NOT NULL CHECK (json_valid(instructions_json) AND json_type(instructions_json) = 'array'),
  schemas_json TEXT NOT NULL CHECK (json_valid(schemas_json) AND json_type(schemas_json) = 'array'),
  permissions_json TEXT NOT NULL CHECK (json_valid(permissions_json) AND json_type(permissions_json) = 'array'),
  workspace_json TEXT NOT NULL CHECK (json_valid(workspace_json) AND json_type(workspace_json) = 'object'),
  capabilities_json TEXT NOT NULL CHECK (json_valid(capabilities_json) AND json_type(capabilities_json) = 'array'),
  digest TEXT NOT NULL CHECK (length(digest) = 64 AND digest NOT GLOB '*[^0-9a-f]*'),
  frozen_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER context_manifests_validate_attempt
BEFORE INSERT ON context_manifests
WHEN NOT EXISTS (
  SELECT 1 FROM attempt_projection
  WHERE attempt_id = NEW.attempt_id AND run_id = NEW.run_id AND node_id = NEW.node_id
)
BEGIN
  SELECT RAISE(ABORT, 'context manifest attempt identity mismatch');
END;

CREATE TRIGGER context_manifests_reject_update
BEFORE UPDATE ON context_manifests
BEGIN
  SELECT RAISE(ABORT, 'context manifests are immutable');
END;

CREATE TRIGGER context_manifests_reject_delete
BEFORE DELETE ON context_manifests
BEGIN
  SELECT RAISE(ABORT, 'context manifests are immutable');
END;

CREATE TABLE capability_records (
  capability_id TEXT PRIMARY KEY CHECK (capability_id <> ''),
  idempotency_key TEXT NOT NULL UNIQUE CHECK (idempotency_key <> ''),
  schema_version INTEGER NOT NULL CHECK (schema_version = 1),
  canonical_name TEXT NOT NULL CHECK (canonical_name <> ''),
  kind TEXT NOT NULL CHECK (kind IN ('skill', 'tool')),
  provenance_class TEXT NOT NULL CHECK (provenance_class IN ('guaranteed', 'registered', 'inherited', 'unsupported_discovery')),
  declared_version TEXT,
  fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64 AND fingerprint NOT GLOB '*[^0-9a-f]*'),
  source_json TEXT NOT NULL CHECK (json_valid(source_json) AND json_type(source_json) = 'object'),
  interfaces_json TEXT NOT NULL CHECK (json_valid(interfaces_json) AND json_type(interfaces_json) = 'object'),
  dependencies_json TEXT NOT NULL CHECK (json_valid(dependencies_json) AND json_type(dependencies_json) = 'array'),
  risk_json TEXT NOT NULL CHECK (json_valid(risk_json) AND json_type(risk_json) = 'object'),
  availability TEXT NOT NULL CHECK (availability IN ('available', 'unavailable', 'unhealthy')),
  observed_at TEXT NOT NULL,
  UNIQUE(canonical_name, kind, provenance_class)
) STRICT;

CREATE INDEX capability_records_resolution
ON capability_records(canonical_name, kind, provenance_class, capability_id);

CREATE TRIGGER capability_records_reject_update
BEFORE UPDATE ON capability_records
BEGIN
  SELECT RAISE(ABORT, 'capability records are immutable');
END;

CREATE TRIGGER capability_records_reject_delete
BEFORE DELETE ON capability_records
BEGIN
  SELECT RAISE(ABORT, 'capability records are immutable');
END;
