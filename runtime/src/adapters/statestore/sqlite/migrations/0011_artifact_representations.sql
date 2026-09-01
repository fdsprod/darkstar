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
