-- Only work already present at this migration retains legacy preparation.
CREATE TABLE source_legacy_work (work_id TEXT PRIMARY KEY REFERENCES native_work_mappings(work_id)) STRICT;
INSERT INTO source_legacy_work SELECT work_id FROM native_work_mappings WHERE mapping_kind='native';
CREATE TABLE source_work_lineages (
  work_id TEXT NOT NULL REFERENCES aggregates(aggregate_id),
  revision INTEGER NOT NULL CHECK(revision>0),
  project_id TEXT NOT NULL,
  binding_revision INTEGER NOT NULL,
  ticket_key TEXT NOT NULL,
  ref_json TEXT NOT NULL CHECK(json_valid(ref_json)),
  origin TEXT NOT NULL CHECK(origin IN ('admitted','legacy_native','native')),
  created_at TEXT NOT NULL,
  PRIMARY KEY(work_id,revision),
  FOREIGN KEY(project_id,binding_revision) REFERENCES backlog_bindings(project_id,revision)
) STRICT;

CREATE TABLE source_work_bindings (
  work_id TEXT PRIMARY KEY,
  lineage_revision INTEGER NOT NULL,
  project_id TEXT NOT NULL,
  ticket_key TEXT NOT NULL,
  UNIQUE(project_id,ticket_key),
  FOREIGN KEY(work_id,lineage_revision) REFERENCES source_work_lineages(work_id,revision)
) STRICT;

CREATE TABLE source_ticket_admissions (
  admission_id TEXT PRIMARY KEY,
  request_key TEXT NOT NULL UNIQUE,
  request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
  work_id TEXT NOT NULL,
  lineage_revision INTEGER NOT NULL,
  project_id TEXT NOT NULL,
  binding_revision INTEGER NOT NULL,
  observation_id TEXT NOT NULL REFERENCES backlog_observations(observation_id),
  approved_at TEXT NOT NULL,
  actor TEXT NOT NULL CHECK(length(actor)>0),
  FOREIGN KEY(work_id,lineage_revision) REFERENCES source_work_lineages(work_id,revision),
  FOREIGN KEY(project_id,binding_revision) REFERENCES backlog_bindings(project_id,revision)
) STRICT;

CREATE TABLE source_work_checks (
  work_id TEXT NOT NULL,
  lineage_revision INTEGER NOT NULL,
  observation_id TEXT NOT NULL REFERENCES backlog_observations(observation_id),
  state TEXT NOT NULL CHECK(state IN ('available','missing','inaccessible')),
  checked_at TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence_ref TEXT NOT NULL,
  pin_json TEXT NOT NULL CHECK(json_valid(pin_json)),
  PRIMARY KEY(work_id,lineage_revision),
  FOREIGN KEY(work_id,lineage_revision) REFERENCES source_work_lineages(work_id,revision),
  CHECK(state!='missing' OR length(evidence_ref)>0)
) STRICT;

CREATE TABLE run_source_snapshots (
  run_id TEXT PRIMARY KEY REFERENCES aggregates(aggregate_id),
  work_id TEXT NOT NULL,
  admission_id TEXT NOT NULL REFERENCES source_ticket_admissions(admission_id),
  observation_id TEXT NOT NULL REFERENCES backlog_observations(observation_id),
  snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
  FOREIGN KEY(work_id) REFERENCES source_work_bindings(work_id)
) STRICT;

CREATE TRIGGER source_lineage_no_update BEFORE UPDATE ON source_work_lineages BEGIN SELECT RAISE(ABORT,'source lineage is immutable'); END;
CREATE TRIGGER source_lineage_no_delete BEFORE DELETE ON source_work_lineages BEGIN SELECT RAISE(ABORT,'source lineage is immutable'); END;
CREATE TRIGGER source_admission_no_update BEFORE UPDATE ON source_ticket_admissions BEGIN SELECT RAISE(ABORT,'source approval is immutable'); END;
CREATE TRIGGER source_admission_no_delete BEFORE DELETE ON source_ticket_admissions BEGIN SELECT RAISE(ABORT,'source approval is immutable'); END;
CREATE TRIGGER run_source_no_update BEFORE UPDATE ON run_source_snapshots BEGIN SELECT RAISE(ABORT,'run source input is immutable'); END;
CREATE TRIGGER run_source_no_delete BEFORE DELETE ON run_source_snapshots BEGIN SELECT RAISE(ABORT,'run source input is immutable'); END;

-- Legacy work creation retains its native mapping. Explicit source admission
-- already points at authoritative native/external content and must not create a
-- shadow native ticket. The event marker survives projection replay.
DROP TRIGGER native_work_created;
CREATE TRIGGER native_work_created AFTER INSERT ON work_item_projection
WHEN NOT EXISTS (SELECT 1 FROM native_work_mappings WHERE work_id=NEW.work_item_id)
AND NOT EXISTS (SELECT 1 FROM events e WHERE e.aggregate_id=NEW.work_item_id AND e.kind='work.created' AND json_extract(e.metadata_json,'$.sourceAdmissionId') IS NOT NULL)
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
