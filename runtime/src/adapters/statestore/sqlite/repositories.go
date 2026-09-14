package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"darkstar/src/core/identity"
	"darkstar/src/ports/statestore"
)

var _ statestore.RepositoryStore = (*Database)(nil)

func (d *Database) Repository(ctx context.Context, id string) (statestore.RepositoryRecord, error) {
	var value statestore.RepositoryRecord
	var encoded string
	err := d.sql.QueryRowContext(ctx, `SELECT record_json FROM repository_registry WHERE repository_id=?`, id).Scan(&encoded)
	if err == nil {
		err = json.Unmarshal([]byte(encoded), &value)
	}
	return value, projectionReadError("repository", id, err)
}

func (d *Database) ProjectRepositories(ctx context.Context, projectID string) ([]statestore.ProjectRepository, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT r.record_json,m.membership_json FROM repository_membership_revisions m
	 JOIN repository_registry r ON r.repository_id=m.repository_id
	 WHERE m.project_id=? AND m.revision=(SELECT MAX(h.revision) FROM repository_membership_revisions h WHERE h.project_id=m.project_id AND h.repository_id=m.repository_id)
	 ORDER BY m.repository_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	values := make([]statestore.ProjectRepository, 0)
	for rows.Next() {
		var record, membership string
		var value statestore.ProjectRepository
		if err := rows.Scan(&record, &membership); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(record), &value.Repository); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(membership), &value.Membership); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (d *Database) MembershipRevision(ctx context.Context, projectID, repositoryID string, revision uint64) (statestore.RepositoryMembership, error) {
	var encoded string
	var value statestore.RepositoryMembership
	err := d.sql.QueryRowContext(ctx, `SELECT membership_json FROM repository_membership_revisions WHERE project_id=? AND repository_id=? AND revision=?`, projectID, repositoryID, revision).Scan(&encoded)
	if err == nil {
		err = json.Unmarshal([]byte(encoded), &value)
	}
	return value, projectionReadError("membership revision", repositoryID, err)
}

func (d *Database) ProjectRepositoryConfiguration(ctx context.Context, projectID string) (statestore.ProjectRepositoryConfiguration, error) {
	return readProjectRepositoryConfiguration(ctx, d.sql, projectID)
}

func readProjectRepositoryConfiguration(ctx context.Context, query rowQueryer, projectID string) (statestore.ProjectRepositoryConfiguration, error) {
	var encoded string
	var value statestore.ProjectRepositoryConfiguration
	err := query.QueryRowContext(ctx, `SELECT configuration_json FROM project_repository_configuration WHERE project_id=?`, projectID).Scan(&encoded)
	if err == nil {
		err = json.Unmarshal([]byte(encoded), &value)
	}
	return value, projectionReadError("project repository configuration", projectID, err)
}

func writeProjectRepositoryConfiguration(ctx context.Context, tx *sql.Tx, projectID string, value statestore.ProjectRepositoryConfiguration) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_repository_configuration VALUES (?,?) ON CONFLICT(project_id) DO UPDATE SET configuration_json=excluded.configuration_json`, projectID, string(encoded))
	return err
}

func applyRepositoryProjection(ctx context.Context, tx *sql.Tx, event statestore.Event) error {
	available, err := repositoryProjectionAvailable(ctx, tx)
	if err != nil {
		return err
	}
	if !available {
		if repositoryEventKind(event.Kind) {
			return errors.New("repository membership migration is required")
		}
		return nil
	}
	switch event.Kind {
	case "project.created":
		var data struct {
			SourceHash             string                        `json:"sourceHash"`
			RepositoryModelVersion int                           `json:"repositoryModelVersion"`
			Defaults               statestore.RepositorySettings `json:"defaults"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		migration := statestore.RepositoryMigration{State: "ready"}
		if data.RepositoryModelVersion == 0 {
			migration = statestore.RepositoryMigration{State: "legacy_unresolved", EvidenceRef: "project:" + event.AggregateID + ":sourceHash:" + data.SourceHash, Reason: "Legacy registration retained only a source hash. Bind verified repository coordinates to restore repository selection."}
		} else if data.RepositoryModelVersion != 2 {
			return errors.New("unsupported project repository model version")
		}
		return writeProjectRepositoryConfiguration(ctx, tx, event.AggregateID, statestore.ProjectRepositoryConfiguration{Defaults: data.Defaults, Migration: migration})
	case "project.repository_defaults_updated":
		var data struct {
			Defaults statestore.RepositorySettings `json:"defaults"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		configuration, err := readProjectRepositoryConfiguration(ctx, tx, event.AggregateID)
		if err != nil {
			return err
		}
		configuration.Defaults = data.Defaults
		return writeProjectRepositoryConfiguration(ctx, tx, event.AggregateID, configuration)
	case "project.repository_set", "project.repository_removed":
		var data struct {
			Repository     statestore.RepositoryRecord     `json:"repository"`
			Membership     statestore.RepositoryMembership `json:"membership"`
			LegacyEvidence string                          `json:"legacyEvidence,omitempty"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		r := data.Repository
		m := data.Membership
		if m.ProjectID != event.AggregateID || m.RepositoryID != r.RepositoryID || m.Label == "" || m.Revision == 0 || r.Root == "" || r.CommonGitDir == "" || r.IdentityKey == "" || r.RepositoryID != identity.Deterministic("repository_", r.IdentityKey) {
			return errors.New("invalid repository membership identity")
		}
		if m.Role != statestore.RepositoryReadOnly && m.Role != statestore.RepositoryImplementation {
			return errors.New("invalid repository membership role")
		}
		if m.UpdatedAt != event.OccurredAt || r.CreatedAt.IsZero() {
			return errors.New("repository membership timestamps are invalid")
		}
		if event.Kind == "project.repository_set" && (m.Status != statestore.MembershipActive || m.Removal != nil) {
			return errors.New("active membership must not contain removal evidence")
		}
		if event.Kind == "project.repository_removed" && (m.Status != statestore.MembershipRemoved || m.Removal == nil || m.Removal.Actor != event.Actor || m.Removal.RemovedAt != event.OccurredAt) {
			return errors.New("removed membership requires exact removal evidence")
		}
		var previousRevision uint64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM repository_membership_revisions WHERE project_id=? AND repository_id=?`, m.ProjectID, m.RepositoryID).Scan(&previousRevision); err != nil {
			return err
		}
		if m.Revision != previousRevision+1 || (previousRevision == 0 && m.Status == statestore.MembershipRemoved) {
			return errors.New("membership revision conflict")
		}
		if m.Status == statestore.MembershipRemoved {
			var priorJSON string
			if err := tx.QueryRowContext(ctx, `SELECT membership_json FROM repository_membership_revisions WHERE project_id=? AND repository_id=? AND revision=?`, m.ProjectID, m.RepositoryID, previousRevision).Scan(&priorJSON); err != nil {
				return err
			}
			var prior statestore.RepositoryMembership
			if err := json.Unmarshal([]byte(priorJSON), &prior); err != nil {
				return err
			}
			if prior.Status != statestore.MembershipActive {
				return errors.New("membership is already removed")
			}
			prior.Revision, prior.Status, prior.Removal, prior.UpdatedAt = m.Revision, m.Status, m.Removal, m.UpdatedAt
			want, _ := json.Marshal(prior)
			got, _ := json.Marshal(m)
			if string(want) != string(got) {
				return errors.New("removal cannot modify membership configuration")
			}
		}
		encodedRepository, err := json.Marshal(r)
		if err != nil {
			return err
		}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT record_json FROM repository_registry WHERE repository_id=?`, r.RepositoryID).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO repository_registry VALUES (?,?,?)`, r.RepositoryID, r.IdentityKey, string(encodedRepository))
		} else if err == nil {
			var registered statestore.RepositoryRecord
			if err := json.Unmarshal([]byte(existing), &registered); err != nil {
				return err
			}
			if registered.IdentityKey != r.IdentityKey {
				return errors.New("repository coordinates are immutable; explicit relocation is required")
			}
		}
		if err != nil {
			return err
		}
		encodedMembership, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO repository_membership_revisions VALUES (?,?,?,?,?,?)`, m.ProjectID, m.RepositoryID, m.Revision, m.Status, m.Role, string(encodedMembership)); err != nil {
			return err
		}
		if strings.TrimSpace(data.LegacyEvidence) != "" {
			configuration, err := readProjectRepositoryConfiguration(ctx, tx, event.AggregateID)
			if err != nil {
				return err
			}
			configuration.Migration = statestore.RepositoryMigration{State: "ready", EvidenceRef: data.LegacyEvidence}
			return writeProjectRepositoryConfiguration(ctx, tx, event.AggregateID, configuration)
		}
	}
	return nil
}

func repositoryProjectionAvailable(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='project_repository_configuration'`).Scan(&count)
	return count != 0, err
}

func clearRepositoryProjections(ctx context.Context, tx *sql.Tx) error {
	available, err := repositoryProjectionAvailable(ctx, tx)
	if err != nil || !available {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM repository_membership_revisions; DELETE FROM repository_registry; DELETE FROM project_repository_configuration`)
	return err
}

func repositoryEventKind(kind string) bool {
	return kind == "project.repository_set" || kind == "project.repository_removed" || kind == "project.repository_defaults_updated"
}
