CREATE TABLE source_admission_rule_pins (
  admission_id TEXT PRIMARY KEY REFERENCES source_ticket_admissions(admission_id),
  snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json))
) STRICT;

CREATE TRIGGER source_rule_pin_no_update BEFORE UPDATE ON source_admission_rule_pins BEGIN SELECT RAISE(ABORT,'source admission rule pin is immutable'); END;
CREATE TRIGGER source_rule_pin_no_delete BEFORE DELETE ON source_admission_rule_pins BEGIN SELECT RAISE(ABORT,'source admission rule pin is immutable'); END;

CREATE TABLE source_intake_cursors (
  identity TEXT PRIMARY KEY,
  cursor_json TEXT NOT NULL CHECK(json_valid(cursor_json))
) STRICT;
