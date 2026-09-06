CREATE TABLE run_execution_contexts (
  run_id TEXT PRIMARY KEY REFERENCES aggregates(aggregate_id) CHECK (run_id GLOB 'run_*'),
  schema_version INTEGER NOT NULL CHECK (schema_version = 1),
  revision INTEGER NOT NULL CHECK (revision >= 1),
  run_inputs_json TEXT NOT NULL CHECK (json_valid(run_inputs_json) AND json_type(run_inputs_json) = 'object'),
  accepted_outputs_json TEXT NOT NULL CHECK (json_valid(accepted_outputs_json) AND json_type(accepted_outputs_json) = 'object'),
  frame_json TEXT NOT NULL CHECK (json_valid(frame_json) AND json_type(frame_json) = 'object'),
  digest TEXT NOT NULL CHECK (length(digest) = 64 AND digest NOT GLOB '*[^0-9a-f]*'),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER run_execution_contexts_validate_run
BEFORE INSERT ON run_execution_contexts
WHEN NOT EXISTS (
  SELECT 1 FROM aggregates WHERE aggregate_id = NEW.run_id AND aggregate_type = 'run'
)
BEGIN
  SELECT RAISE(ABORT, 'run execution context requires a run aggregate');
END;

CREATE TRIGGER run_execution_contexts_run_inputs_immutable
BEFORE UPDATE OF run_inputs_json ON run_execution_contexts
WHEN NEW.run_inputs_json <> OLD.run_inputs_json
BEGIN
  SELECT RAISE(ABORT, 'run execution inputs are immutable');
END;

CREATE TRIGGER run_execution_contexts_run_id_immutable
BEFORE UPDATE OF run_id ON run_execution_contexts
WHEN NEW.run_id <> OLD.run_id
BEGIN
  SELECT RAISE(ABORT, 'run execution context identity is immutable');
END;

CREATE TRIGGER run_execution_contexts_reject_delete
BEFORE DELETE ON run_execution_contexts
BEGIN
  SELECT RAISE(ABORT, 'run execution contexts are durable');
END;
