package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"darkstar/src/core/config"
	"darkstar/src/core/identity"
	"darkstar/src/ports/statestore"
)

var _ statestore.RepositoryScopeStore = (*Database)(nil)
var scopeSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
var scopeGitSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func readScopeJSON(ctx context.Context, q rowQueryer, query, id string, target any) error {
	var encoded string
	err := q.QueryRowContext(ctx, query, id).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(encoded), target)
}

func (d *Database) RepositoryScope(ctx context.Context, id string) (statestore.RepositoryScope, error) {
	var scope statestore.RepositoryScope
	err := readScopeJSON(ctx, d.sql, `SELECT record_json FROM repository_scopes WHERE scope_id=?`, id, &scope)
	return scope, err
}

func validateFrozenScope(scope statestore.RepositoryScope) error {
	if !strings.HasPrefix(scope.ScopeID, "scope_") || !idPayloadPattern.MatchString(strings.TrimPrefix(scope.ScopeID, "scope_")) || scope.SchemaVersion != 1 || scope.ProjectID == "" || scope.ProjectRevision == 0 || scope.CreatedAt.IsZero() || scope.Repositories == nil || scope.ContentPolicy != "committed_only" || !scopeSHA.MatchString(scope.RequestDigest) || scope.Digest != statestore.RepositoryScopeDigest(scope) {
		return errors.New("invalid immutable repository scope")
	}
	if (scope.Mode != statestore.InvestigationScopeNone || len(scope.Repositories) != 0) && (scope.Mode != statestore.InvestigationScopeReadOnly || len(scope.Repositories) < 1 || len(scope.Repositories) > 32) {
		return errors.New("invalid repository scope cardinality")
	}
	previous := ""
	for _, entry := range scope.Repositories {
		if entry.Repository.RepositoryID <= previous || entry.Membership.RepositoryID != entry.Repository.RepositoryID || entry.Membership.ProjectID != scope.ProjectID || entry.Membership.Status != statestore.MembershipActive || entry.Membership.Revision == 0 || strings.TrimSpace(entry.Ref) == "" || !scopeGitSHA.MatchString(entry.Revision.CommitSHA) || !scopeGitSHA.MatchString(entry.Revision.TreeSHA) || !scopeSHA.MatchString(entry.ConfigurationDigest) || !json.Valid([]byte(entry.Configuration)) {
			return errors.New("invalid frozen repository scope entry")
		}
		previous = entry.Repository.RepositoryID
		var configuration config.ResolvedRepositorySettings
		if err := json.Unmarshal([]byte(entry.Configuration), &configuration); err != nil {
			return err
		}
		configurationDigest := statestore.RepositoryScopeContentDigest(struct {
			Settings statestore.RepositorySettings             `json:"settings"`
			Sources  map[string]config.RepositorySettingSource `json:"sources"`
		}{configuration.Settings, configuration.Sources})
		if configuration.Digest != entry.ConfigurationDigest || configurationDigest != entry.ConfigurationDigest {
			return errors.New("frozen repository configuration digest mismatch")
		}
	}
	return nil
}

func (d *Database) FreezeRepositoryScope(ctx context.Context, scope statestore.RepositoryScope) (statestore.RepositoryScope, error) {
	if err := validateFrozenScope(scope); err != nil {
		return statestore.RepositoryScope{}, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.RepositoryScope{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var previous statestore.RepositoryScope
	err = readScopeJSON(ctx, tx, `SELECT record_json FROM repository_scopes WHERE scope_id=?`, scope.ScopeID, &previous)
	if err == nil {
		if previous.RequestDigest != scope.RequestDigest {
			return statestore.RepositoryScope{}, statestore.ErrRepositoryScopeConflict
		}
		return previous, nil
	}
	if !errors.Is(err, statestore.ErrNotFound) {
		return statestore.RepositoryScope{}, err
	}
	var revision uint64
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT resource_version,status FROM project_projection WHERE project_id=?`, scope.ProjectID).Scan(&revision, &status); err != nil {
		return statestore.RepositoryScope{}, err
	}
	if revision != scope.ProjectRevision || status != "active" {
		return statestore.RepositoryScope{}, statestore.ErrRepositoryScopeConflict
	}
	for _, entry := range scope.Repositories {
		var current, record string
		if err = tx.QueryRowContext(ctx, `SELECT m.membership_json,r.record_json FROM repository_membership_revisions m JOIN repository_registry r ON r.repository_id=m.repository_id WHERE m.project_id=? AND m.repository_id=? ORDER BY m.revision DESC LIMIT 1`, scope.ProjectID, entry.Repository.RepositoryID).Scan(&current, &record); err != nil {
			return statestore.RepositoryScope{}, err
		}
		var membership statestore.RepositoryMembership
		var repository statestore.RepositoryRecord
		if err = json.Unmarshal([]byte(current), &membership); err != nil {
			return statestore.RepositoryScope{}, err
		}
		if err = json.Unmarshal([]byte(record), &repository); err != nil {
			return statestore.RepositoryScope{}, err
		}
		if !reflect.DeepEqual(membership, entry.Membership) || repository != entry.Repository {
			return statestore.RepositoryScope{}, statestore.ErrRepositoryScopeConflict
		}
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return statestore.RepositoryScope{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_scopes VALUES(?,?,?,?,?,?)`, scope.ScopeID, scope.ProjectID, scope.Mode, scope.RequestDigest, scope.Digest, string(encoded)); err != nil {
		return statestore.RepositoryScope{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_scope_preparation VALUES(?,1,'preparing','',?)`, scope.ScopeID, formatTime(d.now())); err != nil {
		return statestore.RepositoryScope{}, err
	}
	if err = d.scopeEvent(ctx, tx, "repository_scope.frozen", scope.ScopeID, scope); err != nil {
		return statestore.RepositoryScope{}, err
	}
	return scope, tx.Commit()
}

func (d *Database) scopeEvent(ctx context.Context, tx *sql.Tx, kind, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	seed := kind + "\x00" + key
	event := statestore.PendingEvent{SchemaVersion: 1, ID: identity.Deterministic("event_", seed), AggregateID: identity.Deterministic("operation_", seed), AggregateType: statestore.AggregateOperation, Kind: kind, OccurredAt: d.now(), CorrelationID: key, CommandID: seed, Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "repositoryscope"}, Data: encoded, Metadata: json.RawMessage(`{}`)}
	_, err = d.appendInTransaction(ctx, tx, event)
	return err
}

func (d *Database) RepositoryScopePreparation(ctx context.Context, id string) (statestore.RepositoryScopePreparation, error) {
	return readScopePreparation(ctx, d.sql, id)
}

func readScopePreparation(ctx context.Context, q rowQueryer, id string) (statestore.RepositoryScopePreparation, error) {
	var value statestore.RepositoryScopePreparation
	var updated string
	err := q.QueryRowContext(ctx, `SELECT revision,status,reason,updated_at FROM repository_scope_preparation WHERE scope_id=?`, id).Scan(&value.Revision, &value.Status, &value.Reason, &updated)
	if err == nil {
		value.UpdatedAt, err = parseTime(updated)
	}
	return value, projectionReadError("repository scope preparation", id, err)
}

func (d *Database) SetRepositoryScopePreparation(ctx context.Context, id string, expected uint64, status statestore.RepositoryScopePreparationStatus, reason string) (statestore.RepositoryScopePreparation, error) {
	if (status != statestore.RepositoryScopeReady && status != statestore.RepositoryScopePreparing && status != statestore.RepositoryScopeBlocked) || (status == statestore.RepositoryScopeBlocked && strings.TrimSpace(reason) == "") || (status != statestore.RepositoryScopeBlocked && reason != "") {
		return statestore.RepositoryScopePreparation{}, errors.New("invalid scope preparation state")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.RepositoryScopePreparation{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	current, err := readScopePreparation(ctx, tx, id)
	if err != nil {
		return current, err
	}
	if current.Revision != expected {
		return current, statestore.ErrRepositoryScopeConflict
	}
	if status == statestore.RepositoryScopeReady {
		var count, want int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM repository_scope_evidence WHERE scope_id=?),json_array_length(record_json,'$.repositories') FROM repository_scopes WHERE scope_id=?`, id, id).Scan(&count, &want); err != nil {
			return current, err
		}
		if count != want {
			return current, errors.New("scope evidence is incomplete")
		}
	}
	current.Status = status
	current.Reason = reason
	current.Revision++
	current.UpdatedAt = d.now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE repository_scope_preparation SET revision=?,status=?,reason=?,updated_at=? WHERE scope_id=?`, current.Revision, status, reason, formatTime(current.UpdatedAt), id); err != nil {
		return current, err
	}
	if err = d.scopeEvent(ctx, tx, "repository_scope.preparation_changed", fmt.Sprintf("%s/%d", id, current.Revision), struct {
		ScopeID     string                                `json:"scopeId"`
		Preparation statestore.RepositoryScopePreparation `json:"preparation"`
	}{id, current}); err != nil {
		return current, err
	}
	return current, tx.Commit()
}

func (d *Database) RepositoryScopeEvidence(ctx context.Context, id string) ([]statestore.RepositoryScopeEvidence, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT evidence_json FROM repository_scope_evidence WHERE scope_id=? ORDER BY repository_id`, id)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := make([]statestore.RepositoryScopeEvidence, 0)
	for rows.Next() {
		var encoded string
		var entry statestore.RepositoryScopeEvidence
		if err = rows.Scan(&encoded); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(encoded), &entry); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (d *Database) SaveRepositoryScopeEvidence(ctx context.Context, evidence statestore.RepositoryScopeEvidence) error {
	if evidence.Evidence.Root == "" || !scopeSHA.MatchString(evidence.Evidence.ManifestDigest) || !scopeSHA.MatchString(evidence.Evidence.CacheKey) || evidence.Evidence.FileCount < 0 || evidence.Evidence.TotalBytes < 0 {
		return errors.New("invalid scope evidence")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var scope statestore.RepositoryScope
	if err = readScopeJSON(ctx, tx, `SELECT record_json FROM repository_scopes WHERE scope_id=?`, evidence.ScopeID, &scope); err != nil {
		return err
	}
	found := false
	for _, entry := range scope.Repositories {
		if entry.Repository.RepositoryID == evidence.RepositoryID {
			found = true
		}
	}
	if !found {
		return errors.New("evidence repository is outside frozen scope")
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT evidence_json FROM repository_scope_evidence WHERE scope_id=? AND repository_id=?`, evidence.ScopeID, evidence.RepositoryID).Scan(&previous)
	if err == nil {
		if previous != string(encoded) {
			return statestore.ErrRepositoryScopeConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_scope_evidence VALUES(?,?,?)`, evidence.ScopeID, evidence.RepositoryID, string(encoded)); err != nil {
		return err
	}
	if err = d.scopeEvent(ctx, tx, "repository_scope.evidence_retained", evidence.ScopeID+"/"+evidence.RepositoryID, evidence); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Database) RepositoryScopeAttempt(ctx context.Context, id string) (statestore.RepositoryScopeAttemptBinding, error) {
	var binding statestore.RepositoryScopeAttemptBinding
	err := readScopeJSON(ctx, d.sql, `SELECT binding_json FROM repository_scope_attempts WHERE attempt_id=?`, id, &binding)
	return binding, err
}

func (d *Database) BindRepositoryScopeAttempt(ctx context.Context, binding statestore.RepositoryScopeAttemptBinding) (statestore.RepositoryScopeAttemptBinding, error) {
	if !strings.HasPrefix(binding.AttemptID, "attempt_") || !idPayloadPattern.MatchString(strings.TrimPrefix(binding.AttemptID, "attempt_")) || binding.RepositoryIDs == nil || binding.CreatedAt.IsZero() || binding.Digest != statestore.RepositoryScopeBindingDigest(binding) {
		return binding, errors.New("invalid scope attempt binding")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return binding, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var prior statestore.RepositoryScopeAttemptBinding
	err = readScopeJSON(ctx, tx, `SELECT binding_json FROM repository_scope_attempts WHERE attempt_id=?`, binding.AttemptID, &prior)
	if err == nil {
		if prior.ScopeID != binding.ScopeID || prior.ScopeDigest != binding.ScopeDigest || prior.EvidenceDigest != binding.EvidenceDigest || !reflect.DeepEqual(prior.RepositoryIDs, binding.RepositoryIDs) {
			return prior, statestore.ErrRepositoryScopeConflict
		}
		return prior, nil
	}
	if !errors.Is(err, statestore.ErrNotFound) {
		return binding, err
	}
	var scope statestore.RepositoryScope
	if err = readScopeJSON(ctx, tx, `SELECT record_json FROM repository_scopes WHERE scope_id=?`, binding.ScopeID, &scope); err != nil {
		return binding, err
	}
	preparation, err := readScopePreparation(ctx, tx, binding.ScopeID)
	if err != nil {
		return binding, err
	}
	if preparation.Status != statestore.RepositoryScopeReady || binding.ScopeDigest != scope.Digest {
		return binding, statestore.ErrRepositoryScopeConflict
	}
	evidence := make([]statestore.RepositoryScopeEvidence, 0, len(binding.RepositoryIDs))
	previous := ""
	for _, id := range binding.RepositoryIDs {
		if id <= previous {
			return binding, errors.New("attempt repositories must be a unique sorted subset")
		}
		previous = id
		var encoded string
		var item statestore.RepositoryScopeEvidence
		if err = tx.QueryRowContext(ctx, `SELECT evidence_json FROM repository_scope_evidence WHERE scope_id=? AND repository_id=?`, binding.ScopeID, id).Scan(&encoded); err != nil {
			return binding, err
		}
		if err = json.Unmarshal([]byte(encoded), &item); err != nil {
			return binding, err
		}
		evidence = append(evidence, item)
	}
	if binding.EvidenceDigest != statestore.RepositoryScopeContentDigest(evidence) {
		return binding, errors.New("attempt evidence digest mismatch")
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return binding, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_scope_attempts VALUES(?,?,?)`, binding.AttemptID, binding.ScopeID, string(encoded)); err != nil {
		return binding, err
	}
	if err = d.scopeEvent(ctx, tx, "repository_scope.attempt_bound", binding.AttemptID, binding); err != nil {
		return binding, err
	}
	return binding, tx.Commit()
}
