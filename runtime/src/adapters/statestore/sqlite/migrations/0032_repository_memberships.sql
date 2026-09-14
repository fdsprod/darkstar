-- DS-231: repository coordinates cannot be recovered from a source hash.
-- Keep unresolved evidence addressable until a verified legacy binding is supplied.
CREATE TABLE repository_registry (
  repository_id TEXT PRIMARY KEY,
  identity_key TEXT NOT NULL UNIQUE,
  record_json TEXT NOT NULL CHECK (json_valid(record_json))
) STRICT;

CREATE TABLE project_repository_configuration (
  project_id TEXT PRIMARY KEY,
  configuration_json TEXT NOT NULL CHECK (json_valid(configuration_json))
) STRICT;

CREATE TABLE repository_membership_revisions (
  project_id TEXT NOT NULL,
  repository_id TEXT NOT NULL REFERENCES repository_registry(repository_id),
  revision INTEGER NOT NULL CHECK (revision > 0),
  status TEXT NOT NULL CHECK (status IN ('active','removed')),
  role TEXT NOT NULL CHECK (role IN ('read_only','implementation')),
  membership_json TEXT NOT NULL CHECK (json_valid(membership_json)),
  PRIMARY KEY(project_id,repository_id,revision),
  CHECK ((status='active' AND json_type(membership_json,'$.removal') IS NULL)
    OR (status='removed' AND json_type(membership_json,'$.removal')='object'))
) STRICT;

INSERT INTO project_repository_configuration
SELECT project_id,json_object('defaults',json('{}'),'migration',json_object(
  'state','legacy_unresolved','evidenceRef','project:' || project_id || ':sourceHash:' || source_hash,
  'reason','Legacy registration retained only a source hash. Bind verified repository coordinates to restore repository selection.'))
FROM project_projection;
