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
