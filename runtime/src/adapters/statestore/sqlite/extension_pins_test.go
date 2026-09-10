package sqlite

import (
	"context"
	"strings"
	"testing"

	"darkstar/src/ports/extension"
)

func TestRunExtensionPinsAreDurableAndImmutable(t *testing.T) {
	ctx := context.Background()
	db := openEventTestDatabase(t)
	id := testID("run", 'E')
	createRunAggregate(t, db, id)
	value := executionContextFixture(id)
	ref := extension.Ref{ID: "example/provider", Version: "1.0.0", Digest: strings.Repeat("a", 64)}
	value.Provider = "custom"
	value.ExtensionPins = map[string]extension.Ref{"provider:custom": ref}
	saved, err := db.SaveRunExecutionContext(ctx, value, 0)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := db.RunExecutionContext(ctx, id)
	if err != nil || loaded.ExtensionPins["provider:custom"] != ref {
		t.Fatalf("pins=%v err=%v", loaded.ExtensionPins, err)
	}
	saved.Provider = "replacement"
	if _, err := db.SaveRunExecutionContext(ctx, saved, saved.Revision); err == nil {
		t.Fatal("run provider was changed")
	}
	saved.Provider = "custom"
	ref.Digest = strings.Repeat("b", 64)
	saved.ExtensionPins["provider:custom"] = ref
	if _, err := db.SaveRunExecutionContext(ctx, saved, saved.Revision); err == nil {
		t.Fatal("immutable provider pin changed")
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE run_execution_contexts SET extension_pins_json='{}' WHERE run_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunExecutionContext(ctx, id); err == nil {
		t.Fatal("pin corruption passed integrity check")
	}
}
