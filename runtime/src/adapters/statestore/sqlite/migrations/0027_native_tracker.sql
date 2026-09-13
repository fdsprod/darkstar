-- Native business truth survives execution projection rebuilds. Existing work
-- identifiers remain the ticket identifiers; immutable legacy events stay intact.
CREATE TABLE native_namespaces (
  project_id TEXT PRIMARY KEY,
  binding_revision INTEGER NOT NULL DEFAULT 1 CHECK (binding_revision > 0)
) STRICT;

CREATE TABLE native_tickets (
  ticket_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES native_namespaces(project_id),
  title TEXT NOT NULL CHECK (length(trim(title)) > 0),
  description TEXT NOT NULL,
  business_state TEXT NOT NULL CHECK (business_state IN ('open','active','completed','cancelled')),
  priority INTEGER NOT NULL CHECK (priority >= 0),
  revision INTEGER NOT NULL CHECK (revision > 0),
  assignees_json TEXT NOT NULL CHECK (json_valid(assignees_json)),
  labels_json TEXT NOT NULL CHECK (json_valid(labels_json)),
  relationships_json TEXT NOT NULL CHECK (json_valid(relationships_json)),
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE native_work_mappings (
  work_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  mapping_kind TEXT NOT NULL CHECK (mapping_kind IN ('native','legacy_unresolved')),
  ticket_id TEXT REFERENCES native_tickets(ticket_id),
  mapping_version TEXT NOT NULL,
  evidence_ref TEXT NOT NULL,
  CHECK ((mapping_kind = 'native' AND ticket_id IS NOT NULL AND ticket_id = work_id) OR (mapping_kind = 'legacy_unresolved' AND ticket_id IS NULL))
) STRICT;

CREATE TABLE native_ticket_history (
  ticket_id TEXT NOT NULL REFERENCES native_tickets(ticket_id),
  revision INTEGER NOT NULL,
  kind TEXT NOT NULL,
  evidence_ref TEXT NOT NULL,
  snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json)),
  request_json TEXT NOT NULL CHECK (json_valid(request_json)),
  recorded_at TEXT NOT NULL,
  PRIMARY KEY (ticket_id, revision)
) STRICT;

CREATE TABLE native_ticket_operations (
  operation_id TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL,
  receipt_json TEXT NOT NULL CHECK (json_valid(receipt_json))
) STRICT;

CREATE TRIGGER native_history_no_update BEFORE UPDATE ON native_ticket_history BEGIN SELECT RAISE(ABORT, 'native history is immutable'); END;
CREATE TRIGGER native_history_no_delete BEFORE DELETE ON native_ticket_history BEGIN SELECT RAISE(ABORT, 'native history is immutable'); END;
CREATE TRIGGER native_operation_no_update BEFORE UPDATE ON native_ticket_operations BEGIN SELECT RAISE(ABORT, 'native receipt is immutable'); END;
CREATE TRIGGER native_operation_no_delete BEFORE DELETE ON native_ticket_operations BEGIN SELECT RAISE(ABORT, 'native receipt is immutable'); END;

INSERT INTO native_namespaces(project_id) SELECT project_id FROM project_projection;
INSERT INTO native_tickets
SELECT w.work_item_id,w.project_id,w.title,w.details,w.status,w.priority,1,'[]','[]','[]',w.evidence_json,w.created_at,w.updated_at
FROM work_item_projection w
WHERE NOT EXISTS (SELECT 1 FROM events e JOIN commands c ON c.idempotency_key=e.command_id AND c.scope='work.import' WHERE e.aggregate_id=w.work_item_id AND e.kind='work.created')
AND NOT EXISTS (SELECT 1 FROM external_refs r WHERE r.owner_id=w.work_item_id);

INSERT INTO native_work_mappings
SELECT w.work_item_id,w.project_id,CASE WHEN n.ticket_id IS NULL THEN 'legacy_unresolved' ELSE 'native' END,n.ticket_id,'legacy-business-state/v1','event:' || w.last_global_position
FROM work_item_projection w LEFT JOIN native_tickets n ON n.ticket_id=w.work_item_id;

-- The history format is the durable NativeTicket storage DTO, not a port union.
INSERT INTO native_ticket_history
SELECT ticket_id,revision,'legacy_migration','legacy-business-state/v1',json_object('ID',ticket_id,'ProjectID',project_id,'Title',title,'Description',description,'State',business_state,'Priority',priority,'Revision',revision,'Assignees',json(assignees_json),'Labels',json(labels_json),'Relationships',json(relationships_json),'Evidence',json(evidence_json),'CreatedAt',created_at,'UpdatedAt',updated_at),'{}',updated_at FROM native_tickets;

CREATE TRIGGER native_project_created AFTER INSERT ON project_projection BEGIN
  INSERT OR IGNORE INTO native_namespaces(project_id) VALUES (NEW.project_id);
END;

-- The compatibility command's retained work.created event is its direct user
-- request evidence. This is the shared transactional native creation path.
CREATE TRIGGER native_work_created AFTER INSERT ON work_item_projection
WHEN NOT EXISTS (SELECT 1 FROM native_work_mappings WHERE work_id=NEW.work_item_id)
BEGIN
  INSERT INTO native_tickets
  SELECT NEW.work_item_id,NEW.project_id,NEW.title,NEW.details,'open',NEW.priority,1,'[]','[]','[]',NEW.evidence_json,NEW.created_at,NEW.updated_at
  WHERE NOT EXISTS (SELECT 1 FROM events e JOIN commands c ON c.idempotency_key=e.command_id AND c.scope='work.import' WHERE e.aggregate_id=NEW.work_item_id AND e.kind='work.created')
  AND NOT EXISTS (SELECT 1 FROM external_refs r WHERE r.owner_id=NEW.work_item_id);
  INSERT INTO native_work_mappings
  VALUES (NEW.work_item_id,NEW.project_id,CASE WHEN EXISTS (SELECT 1 FROM native_tickets WHERE ticket_id=NEW.work_item_id) THEN 'native' ELSE 'legacy_unresolved' END,(SELECT ticket_id FROM native_tickets WHERE ticket_id=NEW.work_item_id),'native-create/v1','event:' || NEW.last_global_position);
  INSERT INTO native_ticket_history
  SELECT ticket_id,revision,'created','event:' || NEW.last_global_position,json_object('ID',ticket_id,'ProjectID',project_id,'Title',title,'Description',description,'State',business_state,'Priority',priority,'Revision',revision,'Assignees',json(assignees_json),'Labels',json(labels_json),'Relationships',json(relationships_json),'Evidence',json(evidence_json),'CreatedAt',created_at,'UpdatedAt',updated_at),'{}',updated_at
  FROM native_tickets WHERE ticket_id=NEW.work_item_id;
END;
