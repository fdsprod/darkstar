PRAGMA defer_foreign_keys = ON;

ALTER TABLE approval_projection RENAME TO approval_projection_v1;

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

INSERT INTO approval_projection(
  approval_id, run_id, class, status, scope_digest, policy_digest,
  resource_version, last_global_position, created_at, updated_at)
SELECT approval_id, run_id, class, status, scope_digest, policy_digest,
  resource_version, last_global_position, created_at, updated_at
FROM approval_projection_v1;

DROP TABLE approval_projection_v1;

CREATE UNIQUE INDEX approval_projection_checkpoint_revision
ON approval_projection(checkpoint_id, checkpoint_revision)
WHERE checkpoint_id <> '';

CREATE INDEX approval_projection_run
ON approval_projection(run_id, created_at, approval_id);
