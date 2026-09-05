CREATE TABLE workflow_drafts (
  draft_id TEXT PRIMARY KEY CHECK (draft_id <> ''),
  workflow_name TEXT NOT NULL CHECK (workflow_name <> ''),
  scope TEXT NOT NULL CHECK (scope IN ('user', 'project')),
  scope_reference TEXT NOT NULL CHECK (scope_reference <> ''),
  base_version TEXT,
  revision INTEGER NOT NULL CHECK (revision >= 1),
  document_json TEXT NOT NULL CHECK (json_valid(document_json) AND json_type(document_json) = 'object'),
  layout_json TEXT NOT NULL CHECK (json_valid(layout_json) AND json_type(layout_json) = 'object'),
  document_digest TEXT NOT NULL CHECK (length(document_digest) = 64 AND document_digest NOT GLOB '*[^0-9a-f]*'),
  idempotency_key TEXT NOT NULL UNIQUE CHECK (idempotency_key <> ''),
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX workflow_drafts_name_scope ON workflow_drafts(workflow_name, scope, scope_reference);

CREATE TABLE workflow_archives (
  workflow_name TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  archived_at TEXT NOT NULL,
  PRIMARY KEY(workflow_name, workflow_version),
  FOREIGN KEY(workflow_name, workflow_version) REFERENCES workflow_versions(workflow_name, workflow_version)
) STRICT;

CREATE TABLE workflow_authoring_events (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL CHECK (kind IN ('workflow.draft_created', 'workflow.draft_updated', 'workflow.draft_discarded', 'workflow.version_published', 'workflow.version_archived')),
  draft_id TEXT,
  workflow_name TEXT NOT NULL,
  workflow_version TEXT,
  draft_revision INTEGER,
  occurred_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER workflow_drafts_audit_insert AFTER INSERT ON workflow_drafts BEGIN
  INSERT INTO workflow_authoring_events(kind, draft_id, workflow_name, workflow_version, draft_revision, occurred_at)
  VALUES ('workflow.draft_created', NEW.draft_id, NEW.workflow_name, NEW.base_version, NEW.revision, NEW.updated_at);
END;

CREATE TRIGGER workflow_drafts_audit_update AFTER UPDATE ON workflow_drafts BEGIN
  INSERT INTO workflow_authoring_events(kind, draft_id, workflow_name, workflow_version, draft_revision, occurred_at)
  VALUES ('workflow.draft_updated', NEW.draft_id, NEW.workflow_name, NEW.base_version, NEW.revision, NEW.updated_at);
END;

CREATE TRIGGER workflow_drafts_audit_delete AFTER DELETE ON workflow_drafts BEGIN
  INSERT INTO workflow_authoring_events(kind, draft_id, workflow_name, workflow_version, draft_revision, occurred_at)
  VALUES ('workflow.draft_discarded', OLD.draft_id, OLD.workflow_name, OLD.base_version, OLD.revision, OLD.updated_at);
END;

CREATE TRIGGER workflow_versions_audit_insert AFTER INSERT ON workflow_versions BEGIN
  INSERT INTO workflow_authoring_events(kind, workflow_name, workflow_version, occurred_at)
  VALUES ('workflow.version_published', NEW.workflow_name, NEW.workflow_version, NEW.installed_at);
END;

CREATE TRIGGER workflow_archives_audit_insert AFTER INSERT ON workflow_archives BEGIN
  INSERT INTO workflow_authoring_events(kind, workflow_name, workflow_version, occurred_at)
  VALUES ('workflow.version_archived', NEW.workflow_name, NEW.workflow_version, NEW.archived_at);
END;
