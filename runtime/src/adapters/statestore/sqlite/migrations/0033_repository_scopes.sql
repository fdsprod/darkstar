-- Frozen investigation resources are immutable content records, retained across
-- execution projection rebuilds alongside their atomic event audit evidence.
CREATE TABLE repository_scopes (
 scope_id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 mode TEXT NOT NULL CHECK (mode IN ('none','read_only')),
 request_digest TEXT NOT NULL CHECK (length(request_digest)=64),
 digest TEXT NOT NULL CHECK (length(digest)=64),
 record_json TEXT NOT NULL CHECK (json_valid(record_json)),
 CHECK ((mode='none' AND json_array_length(record_json,'$.repositories')=0)
 OR (mode='read_only' AND json_array_length(record_json,'$.repositories') BETWEEN 1 AND 32))
) STRICT;

CREATE TABLE repository_scope_evidence (
 scope_id TEXT NOT NULL REFERENCES repository_scopes(scope_id),
 repository_id TEXT NOT NULL,
 evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
 PRIMARY KEY(scope_id,repository_id)
) STRICT;

CREATE TABLE repository_scope_preparation (
 scope_id TEXT PRIMARY KEY REFERENCES repository_scopes(scope_id),
 revision INTEGER NOT NULL CHECK (revision>0),
 status TEXT NOT NULL CHECK (status IN ('preparing','ready','blocked')),
 reason TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK ((status='blocked' AND length(trim(reason))>0) OR (status<>'blocked' AND reason=''))
) STRICT;

CREATE TABLE repository_scope_attempts (
 attempt_id TEXT PRIMARY KEY,
 scope_id TEXT NOT NULL REFERENCES repository_scopes(scope_id),
 binding_json TEXT NOT NULL CHECK (json_valid(binding_json))
) STRICT;

CREATE TRIGGER repository_scope_no_update BEFORE UPDATE ON repository_scopes BEGIN SELECT RAISE(ABORT,'repository scope is immutable'); END;
CREATE TRIGGER repository_scope_no_delete BEFORE DELETE ON repository_scopes BEGIN SELECT RAISE(ABORT,'repository scope is immutable'); END;
CREATE TRIGGER repository_scope_evidence_no_update BEFORE UPDATE ON repository_scope_evidence BEGIN SELECT RAISE(ABORT,'repository scope evidence is immutable'); END;
CREATE TRIGGER repository_scope_evidence_no_delete BEFORE DELETE ON repository_scope_evidence BEGIN SELECT RAISE(ABORT,'repository scope evidence is immutable'); END;
CREATE TRIGGER repository_scope_attempt_no_update BEFORE UPDATE ON repository_scope_attempts BEGIN SELECT RAISE(ABORT,'repository attempt binding is immutable'); END;
CREATE TRIGGER repository_scope_attempt_no_delete BEFORE DELETE ON repository_scope_attempts BEGIN SELECT RAISE(ABORT,'repository attempt binding is immutable'); END;
