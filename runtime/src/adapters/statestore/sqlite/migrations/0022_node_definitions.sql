CREATE TABLE node_definitions (
  scope TEXT NOT NULL CHECK (scope IN ('user','project')),
  owner TEXT NOT NULL CHECK (owner <> ''),
  definition_name TEXT NOT NULL CHECK (definition_name <> ''),
  definition_version TEXT NOT NULL CHECK (definition_version <> ''),
  definition_digest TEXT NOT NULL CHECK (length(definition_digest)=64 AND definition_digest NOT GLOB '*[^0-9a-f]*'),
  document_json TEXT NOT NULL CHECK (json_valid(document_json) AND json_type(document_json)='object'),
  created_at TEXT NOT NULL,
  archived_at TEXT,
  PRIMARY KEY(scope,owner,definition_name,definition_version)
) STRICT;

CREATE INDEX node_definitions_discovery ON node_definitions(scope,owner,archived_at,definition_name,definition_version);
