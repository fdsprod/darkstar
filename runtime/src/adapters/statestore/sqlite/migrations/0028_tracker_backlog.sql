-- Selected source, observations and refresh state are business read-side state.
-- They neither create work/execution records nor mirror execution status.
CREATE TABLE backlog_bindings (
  project_id TEXT NOT NULL REFERENCES native_namespaces(project_id),
  revision INTEGER NOT NULL CHECK (revision > 0),
  source_json TEXT NOT NULL CHECK (json_valid(source_json)),
  selected_at TEXT NOT NULL,
  PRIMARY KEY (project_id, revision)
) STRICT;

CREATE TABLE backlog_selected_sources (
  project_id TEXT PRIMARY KEY REFERENCES native_namespaces(project_id),
  binding_revision INTEGER NOT NULL,
  FOREIGN KEY (project_id,binding_revision) REFERENCES backlog_bindings(project_id,revision)
) STRICT;

CREATE TABLE backlog_refreshes (
  project_id TEXT NOT NULL,
  binding_revision INTEGER NOT NULL,
  revision INTEGER NOT NULL CHECK (revision > 0),
  state_json TEXT NOT NULL CHECK (json_valid(state_json)),
  PRIMARY KEY (project_id,binding_revision),
  FOREIGN KEY (project_id,binding_revision) REFERENCES backlog_bindings(project_id,revision)
) STRICT;

CREATE TABLE backlog_observations (
  observation_id TEXT PRIMARY KEY,
  ticket_key TEXT NOT NULL,
  native_revision TEXT NOT NULL CHECK (length(native_revision)>0),
  content_digest TEXT NOT NULL CHECK (length(content_digest)=64),
  ref_json TEXT NOT NULL CHECK (json_valid(ref_json)),
  ticket_json TEXT NOT NULL CHECK (json_valid(ticket_json)),
  observed_at TEXT NOT NULL,
  evidence_ref TEXT NOT NULL CHECK (length(evidence_ref)>0),
  UNIQUE (ticket_key,native_revision)
) STRICT;

CREATE TABLE backlog_cached_tickets (
  project_id TEXT NOT NULL,
  binding_revision INTEGER NOT NULL,
  ticket_key TEXT NOT NULL,
  observation_id TEXT NOT NULL REFERENCES backlog_observations(observation_id),
  state TEXT NOT NULL CHECK (state IN ('available','missing','inaccessible')),
  checked_at TEXT NOT NULL,
  seen_generation INTEGER NOT NULL CHECK (seen_generation>=0),
  reason TEXT NOT NULL,
  evidence_ref TEXT NOT NULL,
  PRIMARY KEY (project_id,binding_revision,ticket_key),
  FOREIGN KEY (project_id,binding_revision) REFERENCES backlog_bindings(project_id,revision),
  CHECK (state!='missing' OR length(evidence_ref)>0)
) STRICT;

CREATE TRIGGER backlog_binding_no_update BEFORE UPDATE ON backlog_bindings BEGIN SELECT RAISE(ABORT,'backlog binding history is immutable'); END;
CREATE TRIGGER backlog_binding_no_delete BEFORE DELETE ON backlog_bindings BEGIN SELECT RAISE(ABORT,'backlog binding history is immutable'); END;
CREATE TRIGGER backlog_observation_no_update BEFORE UPDATE ON backlog_observations BEGIN SELECT RAISE(ABORT,'source observation is immutable'); END;
CREATE TRIGGER backlog_observation_no_delete BEFORE DELETE ON backlog_observations BEGIN SELECT RAISE(ABORT,'source observation is immutable'); END;

INSERT INTO backlog_bindings(project_id,revision,source_json,selected_at)
SELECT project_id,1,json_object('version',1,'kind','built_in','namespace',json_object('Provider','built_in','Host','darkstar.local','TenantID','local','ScopeID',project_id)),COALESCE((SELECT created_at FROM project_projection p WHERE p.project_id=n.project_id),'1970-01-01T00:00:00Z') FROM native_namespaces n;
INSERT INTO backlog_selected_sources SELECT project_id,1 FROM native_namespaces;

CREATE TRIGGER backlog_namespace_created AFTER INSERT ON native_namespaces BEGIN
  INSERT INTO backlog_bindings VALUES (NEW.project_id,1,json_object('version',1,'kind','built_in','namespace',json_object('Provider','built_in','Host','darkstar.local','TenantID','local','ScopeID',NEW.project_id)),COALESCE((SELECT created_at FROM project_projection WHERE project_id=NEW.project_id),'1970-01-01T00:00:00Z'));
  INSERT INTO backlog_selected_sources VALUES (NEW.project_id,1);
END;
