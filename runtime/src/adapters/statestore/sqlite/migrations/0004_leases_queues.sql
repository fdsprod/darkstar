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
