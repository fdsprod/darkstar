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
