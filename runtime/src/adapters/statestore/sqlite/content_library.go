package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"darkstar/src/ports/contentstore"
)

var _ contentstore.Store = (*Database)(nil)

const contentItemSelect = `SELECT id,name,description,kind,archived_at,draft_revision,draft_json FROM content_library_items`

func (d *Database) ContentItems(ctx context.Context) ([]contentstore.Item, error) {
	rows, err := d.sql.QueryContext(ctx, contentItemSelect+` ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	items := []contentstore.Item{}
	for rows.Next() {
		item, err := scanContentItem(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for index := range items {
		versions, err := readContentVersions(ctx, d.sql, items[index].ID)
		if err != nil {
			return nil, err
		}
		items[index].Versions = versions
	}
	return items, nil
}

func (d *Database) ContentItem(ctx context.Context, id string) (contentstore.Item, error) {
	item, err := scanContentItem(d.sql.QueryRowContext(ctx, contentItemSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, contentstore.ErrNotFound
	}
	if err != nil {
		return item, err
	}
	item.Versions, err = readContentVersions(ctx, d.sql, id)
	return item, err
}

func scanContentItem(row rowScanner) (contentstore.Item, error) {
	var item contentstore.Item
	var archived sql.NullString
	var document string
	if err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Kind, &archived, &item.Draft.Revision, &document); err != nil {
		return item, err
	}
	if err := json.Unmarshal([]byte(document), &item.Draft.Document); err != nil {
		return item, err
	}
	if archived.Valid {
		value, err := parseTime(archived.String)
		if err != nil {
			return item, err
		}
		item.ArchivedAt = &value
	}
	return item, item.Draft.Document.Validate()
}

type contentQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readContentVersions(ctx context.Context, q contentQuerier, id string) ([]contentstore.Version, error) {
	rows, err := q.QueryContext(ctx, `SELECT version,digest,document_json,created_at FROM content_library_versions WHERE item_id=? ORDER BY created_at,version`, id)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	versions := []contentstore.Version{}
	for rows.Next() {
		version := contentstore.Version{Reference: contentstore.Reference{ID: id}}
		var document, created string
		if err := rows.Scan(&version.Reference.Version, &version.Reference.Digest, &document, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(document), &version.Document); err != nil {
			return nil, err
		}
		version.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		if err := validateContentVersion(version, id); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func validateContentVersion(version contentstore.Version, id string) error {
	if err := version.Reference.Validate(); err != nil {
		return err
	}
	if err := version.Document.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(version.Document)
	hash := sha256.Sum256(raw)
	if version.Reference.ID != id || hex.EncodeToString(hash[:]) != version.Reference.Digest || version.CreatedAt.IsZero() {
		return errors.New("content version integrity check failed")
	}
	return nil
}

func (d *Database) SaveContentItem(ctx context.Context, item contentstore.Item, expected uint64) (err error) {
	if item.ID == "" || strings.TrimSpace(item.Name) == "" || item.Draft.Revision != expected+1 || item.Kind != item.Draft.Document.Kind {
		return errors.New("invalid content identity or revision")
	}
	if err := item.Draft.Document.Validate(); err != nil {
		return err
	}
	incoming := map[string]contentstore.Version{}
	for _, version := range item.Versions {
		if err := validateContentVersion(version, item.ID); err != nil {
			return err
		}
		if version.Document.Kind != item.Kind {
			return errors.New("content version kind cannot change")
		}
		if _, exists := incoming[version.Reference.Version]; exists {
			return errors.New("duplicate content version")
		}
		incoming[version.Reference.Version] = version
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	existing, readErr := scanContentItem(tx.QueryRowContext(ctx, contentItemSelect+` WHERE id=?`, item.ID))
	if errors.Is(readErr, sql.ErrNoRows) && expected != 0 || readErr == nil && existing.Draft.Revision != expected {
		return contentstore.ErrConflict
	}
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return readErr
	}
	if readErr == nil && item.Kind != existing.Kind {
		return errors.New("content kind is immutable")
	}
	versions, err := readContentVersions(ctx, tx, item.ID)
	if err != nil {
		return err
	}
	for _, version := range versions {
		previous, _ := json.Marshal(version)
		candidate, _ := json.Marshal(incoming[version.Reference.Version])
		if string(previous) != string(candidate) {
			return fmt.Errorf("%w: published version cannot change or disappear", contentstore.ErrConflict)
		}
		delete(incoming, version.Reference.Version)
	}
	raw, _ := json.Marshal(item.Draft.Document)
	var archived any
	if item.ArchivedAt != nil {
		archived = formatTime(*item.ArchivedAt)
	}
	if errors.Is(readErr, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO content_library_items(id,name,description,kind,archived_at,draft_revision,draft_json) VALUES(?,?,?,?,?,?,?)`, item.ID, item.Name, item.Description, item.Kind, archived, item.Draft.Revision, string(raw))
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE content_library_items SET name=?,description=?,archived_at=?,draft_revision=?,draft_json=? WHERE id=? AND draft_revision=?`, item.Name, item.Description, archived, item.Draft.Revision, string(raw), item.ID, expected)
	}
	if err != nil {
		return err
	}
	for _, version := range incoming {
		raw, _ := json.Marshal(version.Document)
		if _, err = tx.ExecContext(ctx, `INSERT INTO content_library_versions(item_id,version,digest,document_json,created_at) VALUES(?,?,?,?,?)`, item.ID, version.Reference.Version, version.Reference.Digest, string(raw), formatTime(version.CreatedAt)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO content_library_events(item_id,revision,occurred_at) VALUES(?,?,?)`, item.ID, item.Draft.Revision, formatTime(d.now())); err != nil {
		return err
	}
	return tx.Commit()
}
