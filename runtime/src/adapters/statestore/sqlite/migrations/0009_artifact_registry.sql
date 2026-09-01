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
