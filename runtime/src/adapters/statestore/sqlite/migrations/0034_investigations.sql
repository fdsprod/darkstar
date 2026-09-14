CREATE TABLE investigations (
 investigation_id TEXT PRIMARY KEY,
 record_json TEXT NOT NULL CHECK(json_valid(record_json)),
 CHECK(json_extract(record_json,'$.id')=investigation_id),
 CHECK(json_extract(record_json,'$.status') IN ('prepared','running','cancelling','succeeded','partial','failed','cancelled')),
 CHECK(json_extract(record_json,'$.revision')>=1),
 CHECK(json_extract(record_json,'$.concurrency') BETWEEN 1 AND 8)
) STRICT;
CREATE TABLE investigation_units (
 unit_id TEXT PRIMARY KEY,
 investigation_id TEXT NOT NULL REFERENCES investigations(investigation_id),
 record_json TEXT NOT NULL CHECK(json_valid(record_json)),
 CHECK(json_extract(record_json,'$.id')=unit_id),
 CHECK(json_extract(record_json,'$.collectionId')=investigation_id),
 CHECK(json_extract(record_json,'$.kind') IN ('repository','synthesis')),
 CHECK(json_extract(record_json,'$.status') IN ('pending','running','uncertain','succeeded','failed','cancelled'))
) STRICT;
CREATE TABLE investigation_attempts (
 attempt_id TEXT PRIMARY KEY,
 investigation_id TEXT NOT NULL REFERENCES investigations(investigation_id),
 unit_id TEXT NOT NULL REFERENCES investigation_units(unit_id),
 record_json TEXT NOT NULL CHECK(json_valid(record_json)),
 CHECK(json_extract(record_json,'$.id')=attempt_id),
 CHECK(json_extract(record_json,'$.collectionId')=investigation_id),
 CHECK(json_extract(record_json,'$.unitId')=unit_id),
 CHECK(json_extract(record_json,'$.state') IN ('running','uncertain','succeeded','failed','cancelled'))
) STRICT;
CREATE INDEX investigation_active_attempts ON investigation_attempts(json_extract(record_json,'$.state'),json_extract(record_json,'$.leaseExpiresAt'));
CREATE TABLE investigation_commands (
 investigation_id TEXT NOT NULL REFERENCES investigations(investigation_id),
 command_key TEXT NOT NULL,
 command TEXT NOT NULL CHECK(command IN ('start','retry','cancel')),
 expected_revision INTEGER NOT NULL,
 PRIMARY KEY(investigation_id,command_key)
) STRICT;
CREATE TABLE investigation_observations (
 attempt_id TEXT NOT NULL REFERENCES investigation_attempts(attempt_id),
 observation_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('prepared','handle','event','submission','result')),
 record_json TEXT NOT NULL CHECK(json_valid(record_json)),
 PRIMARY KEY(attempt_id,observation_id)
) STRICT;
CREATE TRIGGER investigation_spec_immutable BEFORE UPDATE ON investigations
 WHEN json_remove(OLD.record_json,'$.status','$.revision','$.updatedAt')<>json_remove(NEW.record_json,'$.status','$.revision','$.updatedAt')
 BEGIN SELECT RAISE(ABORT,'investigation specification is immutable'); END;
CREATE TRIGGER investigation_no_delete BEFORE DELETE ON investigations BEGIN SELECT RAISE(ABORT,'investigation history is retained'); END;
CREATE TRIGGER investigation_unit_no_delete BEFORE DELETE ON investigation_units BEGIN SELECT RAISE(ABORT,'investigation history is retained'); END;
CREATE TRIGGER investigation_attempt_no_delete BEFORE DELETE ON investigation_attempts BEGIN SELECT RAISE(ABORT,'investigation history is retained'); END;
CREATE TRIGGER investigation_observation_no_update BEFORE UPDATE ON investigation_observations BEGIN SELECT RAISE(ABORT,'investigation observations are immutable'); END;
CREATE TRIGGER investigation_observation_no_delete BEFORE DELETE ON investigation_observations BEGIN SELECT RAISE(ABORT,'investigation observations are immutable'); END;
