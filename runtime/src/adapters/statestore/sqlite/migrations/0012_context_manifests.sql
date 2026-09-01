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
