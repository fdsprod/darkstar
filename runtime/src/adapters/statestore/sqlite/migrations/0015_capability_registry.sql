CREATE TABLE capability_records (
  capability_id TEXT PRIMARY KEY CHECK (capability_id <> ''),
  idempotency_key TEXT NOT NULL UNIQUE CHECK (idempotency_key <> ''),
  schema_version INTEGER NOT NULL CHECK (schema_version = 1),
  canonical_name TEXT NOT NULL CHECK (canonical_name <> ''),
  kind TEXT NOT NULL CHECK (kind IN ('skill', 'tool')),
  provenance_class TEXT NOT NULL CHECK (provenance_class IN ('guaranteed', 'registered', 'inherited', 'unsupported_discovery')),
  declared_version TEXT,
  fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64 AND fingerprint NOT GLOB '*[^0-9a-f]*'),
  source_json TEXT NOT NULL CHECK (json_valid(source_json) AND json_type(source_json) = 'object'),
  interfaces_json TEXT NOT NULL CHECK (json_valid(interfaces_json) AND json_type(interfaces_json) = 'object'),
  dependencies_json TEXT NOT NULL CHECK (json_valid(dependencies_json) AND json_type(dependencies_json) = 'array'),
  risk_json TEXT NOT NULL CHECK (json_valid(risk_json) AND json_type(risk_json) = 'object'),
  availability TEXT NOT NULL CHECK (availability IN ('available', 'unavailable', 'unhealthy')),
  observed_at TEXT NOT NULL,
  UNIQUE(canonical_name, kind, provenance_class)
) STRICT;

CREATE INDEX capability_records_resolution
ON capability_records(canonical_name, kind, provenance_class, capability_id);

CREATE TRIGGER capability_records_reject_update
BEFORE UPDATE ON capability_records
BEGIN
  SELECT RAISE(ABORT, 'capability records are immutable');
END;

CREATE TRIGGER capability_records_reject_delete
BEFORE DELETE ON capability_records
BEGIN
  SELECT RAISE(ABORT, 'capability records are immutable');
END;
