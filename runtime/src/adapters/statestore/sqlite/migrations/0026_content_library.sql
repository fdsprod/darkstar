CREATE TABLE content_library_items (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL CHECK(length(trim(name))>0),
 description TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('template','prompt')),
 archived_at TEXT,
 draft_revision INTEGER NOT NULL CHECK(draft_revision>0),
 draft_json TEXT NOT NULL CHECK(json_valid(draft_json) AND json_type(draft_json)='object' AND json_extract(draft_json,'$.kind')=kind)
) STRICT;

CREATE TABLE content_library_versions (
 item_id TEXT NOT NULL REFERENCES content_library_items(id),
 version TEXT NOT NULL,
 digest TEXT NOT NULL CHECK(length(digest)=64 AND digest NOT GLOB '*[^0-9a-f]*'),
 document_json TEXT NOT NULL CHECK(json_valid(document_json) AND json_type(document_json)='object'),
 created_at TEXT NOT NULL,
 PRIMARY KEY(item_id,version)
) STRICT;

CREATE TRIGGER content_library_versions_no_update BEFORE UPDATE ON content_library_versions
BEGIN SELECT RAISE(ABORT,'published content versions are immutable'); END;
CREATE TRIGGER content_library_versions_no_delete BEFORE DELETE ON content_library_versions
BEGIN SELECT RAISE(ABORT,'published content versions are retained'); END;

CREATE TABLE content_library_events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 item_id TEXT NOT NULL REFERENCES content_library_items(id),
 revision INTEGER NOT NULL CHECK(revision>0),
 occurred_at TEXT NOT NULL,
 UNIQUE(item_id,revision)
) STRICT;
CREATE TRIGGER content_library_events_no_update BEFORE UPDATE ON content_library_events
BEGIN SELECT RAISE(ABORT,'content events are immutable'); END;
CREATE TRIGGER content_library_events_no_delete BEFORE DELETE ON content_library_events
BEGIN SELECT RAISE(ABORT,'content events are retained'); END;

ALTER TABLE run_execution_contexts ADD COLUMN prompt_snapshots_json TEXT NOT NULL DEFAULT '{}'
 CHECK(json_valid(prompt_snapshots_json) AND json_type(prompt_snapshots_json)='object');
