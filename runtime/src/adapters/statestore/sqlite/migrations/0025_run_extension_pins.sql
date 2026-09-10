ALTER TABLE run_execution_contexts ADD COLUMN extension_pins_json TEXT NOT NULL DEFAULT '{}'
  CHECK (json_valid(extension_pins_json) AND json_type(extension_pins_json) = 'object');

ALTER TABLE artifact_representations ADD COLUMN processor_digest TEXT NOT NULL DEFAULT ''
  CHECK (processor_digest = '' OR (length(processor_digest) = 64 AND processor_digest NOT GLOB '*[^0-9a-f]*'));

ALTER TABLE run_execution_contexts ADD COLUMN provider_name TEXT NOT NULL DEFAULT '';
