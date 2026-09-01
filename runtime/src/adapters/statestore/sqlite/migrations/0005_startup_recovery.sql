CREATE TABLE recovery_decisions (
  startup_id TEXT NOT NULL,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('lease', 'operation')),
  subject_id TEXT NOT NULL,
  subject_authority TEXT NOT NULL,
  subject_state TEXT NOT NULL,
  subject_payload_json TEXT NOT NULL CHECK (json_valid(subject_payload_json) AND json_type(subject_payload_json) = 'object'),
  outcome TEXT NOT NULL CHECK (outcome IN ('adopt', 'resume', 'retry', 'interrupt', 'reconcile_required')),
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND json_type(evidence_json) = 'object'),
  recorded_at TEXT NOT NULL,
  PRIMARY KEY(startup_id, subject_kind, subject_id)
) STRICT;

CREATE INDEX recovery_decisions_subject
ON recovery_decisions(subject_kind, subject_id, recorded_at);

CREATE TRIGGER recovery_decisions_reject_update
BEFORE UPDATE ON recovery_decisions
BEGIN
  SELECT RAISE(ABORT, 'recovery decisions are append-only');
END;

CREATE TRIGGER recovery_decisions_reject_delete
BEFORE DELETE ON recovery_decisions
BEGIN
  SELECT RAISE(ABORT, 'recovery decisions are append-only');
END;
