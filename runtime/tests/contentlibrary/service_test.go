package contentlibrary_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/contentlibrary"
)

func TestLibraryRetainsPinnedVersionsAcrossEditsArchiveAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "library.db")
	database, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service := contentlibrary.New(database)
	original := contentlibrary.Document{Kind: "template", Content: "# Behavior\nOriginal", RequiredHeadings: []string{"Behavior"}}
	item, err := service.Create(ctx, contentlibrary.CreateRequest{Name: "Design", Document: original})
	if err != nil {
		t.Fatal(err)
	}
	item, err = service.Publish(ctx, item.ID, contentlibrary.PublishRequest{ExpectedRevision: 1, Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	pinned := item.Versions[0].Reference
	revised := contentlibrary.Document{Kind: "template", Content: "# Behavior\nRevised", RequiredHeadings: []string{"Behavior"}}
	if _, err := service.Update(ctx, item.ID, contentlibrary.UpdateRequest{ExpectedRevision: 1, Document: revised}); !errors.Is(err, contentlibrary.ErrConflict) {
		t.Fatalf("stale editor overwrite: %v", err)
	}
	item, err = service.Update(ctx, item.ID, contentlibrary.UpdateRequest{ExpectedRevision: 2, Document: revised})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, item.ID, contentlibrary.PublishRequest{ExpectedRevision: 3, Version: "1.0.0"}); !errors.Is(err, contentlibrary.ErrConflict) {
		t.Fatalf("republished version: %v", err)
	}
	item, err = service.Publish(ctx, item.ID, contentlibrary.PublishRequest{ExpectedRevision: 3, Version: "1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = service.Archive(ctx, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, item.ID, contentlibrary.UpdateRequest{ExpectedRevision: item.Draft.Revision, Document: original}); !errors.Is(err, contentlibrary.ErrConflict) {
		t.Fatalf("edited archived item: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	service = contentlibrary.New(database)
	version, err := service.Resolve(ctx, pinned)
	if err != nil || version.Document.Content != original.Content {
		t.Fatalf("pinned artifact changed across restart: %#v %v", version, err)
	}
	wrong := pinned
	wrong.Digest = strings.Repeat("0", 64)
	if _, err := service.Resolve(ctx, wrong); !errors.Is(err, contentlibrary.ErrConflict) {
		t.Fatalf("mismatched digest accepted: %v", err)
	}
	restored, err := service.Archive(ctx, item.ID, false)
	if err != nil || restored.ArchivedAt != nil || len(restored.Versions) != 2 {
		t.Fatalf("restore lost history: %#v %v", restored, err)
	}
	duplicate, err := service.Duplicate(ctx, item.ID, "Alternate design")
	if err != nil || duplicate.ID == item.ID || len(duplicate.Versions) != 0 || duplicate.Draft.Document.Content != revised.Content {
		t.Fatalf("duplicate: %#v %v", duplicate, err)
	}
}

func TestSeedPreservesUserEditsAndPublishedRowsCannotBeMutated(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "library.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	service := contentlibrary.New(database)
	if err := service.Seed(ctx, contentlibrary.BuiltinItems()); err != nil {
		t.Fatal(err)
	}
	item := contentlibrary.BuiltinItems()[0]
	changed := item.Draft.Document
	changed.Instructions += "\nUser instruction."
	if changed.Kind == "template" {
		changed.Instructions = ""
		changed.Content += "\nUser section."
	}
	if _, err := service.Update(ctx, item.ID, contentlibrary.UpdateRequest{ExpectedRevision: 1, Document: changed}); err != nil {
		t.Fatal(err)
	}
	if err := service.Seed(ctx, contentlibrary.BuiltinItems()); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Get(ctx, item.ID)
	if err != nil || loaded.Draft.Revision != 2 {
		t.Fatalf("seed replaced draft: %#v %v", loaded, err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE content_library_versions SET digest=? WHERE item_id=?`, strings.Repeat("0", 64), item.ID); err == nil {
		t.Fatal("direct published-version update succeeded")
	}
	if _, err := database.SQL().ExecContext(ctx, `DELETE FROM content_library_versions WHERE item_id=?`, item.ID); err == nil {
		t.Fatal("direct published-version delete succeeded")
	}
	loaded.Versions = nil
	loaded.Draft.Revision++
	if err := database.SaveContentItem(ctx, loaded, 2); !errors.Is(err, contentlibrary.ErrConflict) {
		t.Fatalf("writer could drop published history: %v", err)
	}
}
