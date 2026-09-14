-- Investigation artifacts retain their exact collection/unit/attempt origin.
-- The common version row uses the legacy operation storage variant; this
-- immutable subtype is joined by all canonical artifact registry projections.
CREATE TABLE investigation_artifact_provenance (
  artifact_id TEXT NOT NULL,
  version INTEGER NOT NULL CHECK (version > 0),
  collection_id TEXT NOT NULL CHECK (length(trim(collection_id)) > 0),
  unit_id TEXT NOT NULL CHECK (length(trim(unit_id)) > 0),
  attempt_id TEXT NOT NULL CHECK (length(trim(attempt_id)) > 0),
  PRIMARY KEY (artifact_id, version),
  FOREIGN KEY (artifact_id, version) REFERENCES artifact_versions(artifact_id, version)
) STRICT;

CREATE TRIGGER investigation_artifact_origin_check
BEFORE INSERT ON investigation_artifact_provenance
WHEN NOT EXISTS (
  SELECT 1 FROM artifact_versions
  WHERE artifact_id = NEW.artifact_id AND version = NEW.version
    AND origin_kind = 'operation' AND source_kind = 'generated'
)
BEGIN
  SELECT RAISE(ABORT, 'investigation provenance requires generated operation storage');
END;

CREATE TRIGGER investigation_artifact_provenance_no_update
BEFORE UPDATE ON investigation_artifact_provenance
BEGIN
  SELECT RAISE(ABORT, 'investigation artifact provenance is immutable');
END;

CREATE TRIGGER investigation_artifact_provenance_no_delete
BEFORE DELETE ON investigation_artifact_provenance
BEGIN
  SELECT RAISE(ABORT, 'investigation artifact provenance is immutable');
END;
