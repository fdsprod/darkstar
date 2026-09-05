package configmutation_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	configfs "darkstar/src/adapters/configurationstore/filesystem"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
	"darkstar/src/core/identity"
	configfiles "darkstar/src/daemon/configuration"
	"darkstar/src/ports/configurationstore"
	"darkstar/src/ports/statestore"
)

func TestApplyPreviewReplayAuditAndSecretRedaction(t *testing.T) {
	t.Parallel()
	service, database, root := newService(t)
	ctx := context.Background()
	state, err := service.State(ctx, config.UserMutationScope())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "codex.exe")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := configmutation.MutationRequest{Scope: config.UserMutationScope(), Key: "provider.codex.executable", Change: configmutation.Set(config.PathValue(executable)), ExpectedRevision: state.Revision}
	preview, err := service.Preview(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || preview.Before.Revision != state.Revision || preview.After.Revision == state.Revision || preview.Restart != config.RestartDaemon {
		t.Fatalf("preview = %#v", preview)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("preview wrote configuration: %v", err)
	}
	applied, err := service.Apply(ctx, configmutation.ApplyRequest{MutationRequest: request, IdempotencyKey: "apply-executable"})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Replayed || applied.State.Revision != preview.After.Revision {
		t.Fatalf("applied = %#v", applied)
	}
	replayed, err := service.Apply(ctx, configmutation.ApplyRequest{MutationRequest: request, IdempotencyKey: "apply-executable"})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.State.Revision != applied.State.Revision {
		t.Fatalf("replay = %#v", replayed)
	}
	stale := request
	stale.Change = configmutation.Unset()
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.Apply(ctx, configmutation.ApplyRequest{MutationRequest: stale, IdempotencyKey: "stale-revision-key"}); !errors.Is(err, configurationstore.ErrRevisionConflict) {
			t.Fatalf("stale replay %d error = %v", attempt, err)
		}
	}
	canary := "DAR130-SERVICE-SECRET-CANARY"
	secret, err := service.WriteSecret(ctx, configmutation.SecretWriteRequest{Name: "codex-api-key", Value: canary, ExpectedRevision: state.SecretRevision, IdempotencyKey: "secret-write-key"})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != "codex-api-key" || secret.Revision == state.SecretRevision {
		t.Fatalf("secret receipt = %#v", secret)
	}
	events, err := database.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), canary) {
		t.Fatal("secret leaked into event audit")
	}
	if !strings.Contains(string(encoded), "configuration.change_recorded") || !strings.Contains(string(encoded), "secret:codex-api-key") {
		t.Fatalf("missing sanitized audit: %s", encoded)
	}
	if strings.Count(string(encoded), "configuration.change_rejected") != 1 {
		t.Fatalf("rejected idempotent replay duplicated audit: %s", encoded)
	}
}

func TestProjectIdentityScopeAndValidationFailures(t *testing.T) {
	t.Parallel()
	service, database, root := newService(t)
	ctx := context.Background()
	projectID := identity.Random("project_")
	now := time.Now().UTC()
	sourceHash := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(root))))
	data, _ := json.Marshal(map[string]any{"name": "test", "sourceHash": sourceHash})
	_, err := database.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateProject, AggregateID: projectID, ExpectedRevision: 0, Kind: "project.created", OccurredAt: now, CorrelationID: projectID, CommandID: "register-project", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "test"}, Data: data, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := config.ProjectMutationScope(projectID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.State(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	request := configmutation.MutationRequest{Scope: scope, Key: "provider.codex.actionAvailability", Change: configmutation.Set(config.EnumValue("disabled")), ExpectedRevision: state.Revision}
	result, err := service.Apply(ctx, configmutation.ApplyRequest{MutationRequest: request, IdempotencyKey: "project-setting"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Scope.ProjectID() != projectID || len(result.State.Configured) != 1 {
		t.Fatalf("project state = %#v", result.State)
	}
	wrong, _ := config.ProjectMutationScope(identity.Random("project_"))
	if _, err := service.State(ctx, wrong); err != configmutation.ErrProjectNotFound {
		t.Fatalf("unknown project error = %v", err)
	}
	bad := request
	bad.Change = configmutation.Set(config.EnumValue("maybe"))
	bad.ExpectedRevision = result.State.Revision
	if _, err := service.Preview(ctx, bad); err == nil {
		t.Fatal("invalid enum preview succeeded")
	}
}

func newService(t *testing.T) (*configmutation.Service, *sqlite.Database, string) {
	t.Helper()
	root := t.TempDir()
	locations := configfiles.FileLocations{UserConfig: filepath.Join(root, "config", "config.yaml"), UserSecrets: filepath.Join(root, "config", "secrets.yaml"), ProjectConfig: filepath.Join(root, ".darkstar", "config.yaml")}
	files, err := configfs.New(locations, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(context.Background(), filepath.Join(root, "data", "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := configmutation.New(files, database, root)
	if err != nil {
		t.Fatal(err)
	}
	return service, database, root
}
