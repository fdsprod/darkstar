CREATE TABLE tracker_mapping_revisions (
    project_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    binding_revision INTEGER NOT NULL CHECK (binding_revision > 0),
    rules_json TEXT NOT NULL CHECK (json_valid(rules_json)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, revision),
    UNIQUE (project_id, binding_revision, revision),
    FOREIGN KEY (project_id, binding_revision) REFERENCES backlog_bindings(project_id, revision)
);

CREATE TABLE tracker_mapping_active (
    project_id TEXT NOT NULL,
    binding_revision INTEGER NOT NULL,
    mapping_revision INTEGER NOT NULL,
    activated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, binding_revision),
    FOREIGN KEY (project_id, binding_revision, mapping_revision)
        REFERENCES tracker_mapping_revisions(project_id, binding_revision, revision)
);

CREATE TABLE tracker_mapping_activations (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    binding_revision INTEGER NOT NULL,
    mapping_revision INTEGER NOT NULL,
    previous_revision INTEGER NOT NULL CHECK (previous_revision >= 0),
    activated_at TEXT NOT NULL,
    FOREIGN KEY (project_id, binding_revision, mapping_revision)
        REFERENCES tracker_mapping_revisions(project_id, binding_revision, revision)
);

CREATE TRIGGER tracker_mapping_revisions_immutable_update BEFORE UPDATE ON tracker_mapping_revisions BEGIN SELECT RAISE(ABORT, 'tracker mapping revisions are immutable'); END;
CREATE TRIGGER tracker_mapping_revisions_immutable_delete BEFORE DELETE ON tracker_mapping_revisions BEGIN SELECT RAISE(ABORT, 'tracker mapping revisions are immutable'); END;
CREATE TRIGGER tracker_mapping_activations_immutable_update BEFORE UPDATE ON tracker_mapping_activations BEGIN SELECT RAISE(ABORT, 'tracker mapping activations are immutable'); END;
CREATE TRIGGER tracker_mapping_activations_immutable_delete BEFORE DELETE ON tracker_mapping_activations BEGIN SELECT RAISE(ABORT, 'tracker mapping activations are immutable'); END;
