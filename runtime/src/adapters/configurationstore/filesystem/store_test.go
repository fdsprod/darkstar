package filesystem_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"darkstar/src/adapters/configurationstore/filesystem"
	configfiles "darkstar/src/daemon/configuration"
	"darkstar/src/ports/configurationstore"
)

func TestPreviewApplyCASAndExactRestore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	user := filepath.Join(root, "config", "config.yaml")
	project := filepath.Join(root, "project", ".darkstar", "config.yaml")
	secrets := filepath.Join(root, "config", "secrets.yaml")
	store := mustStore(t, user, project, secrets, filepath.Join(root, "data"))
	ctx := context.Background()
	initial, err := store.Snapshot(ctx, configurationstore.TargetUser)
	if err != nil {
		t.Fatal(err)
	}
	mutation := configurationstore.Mutation{Operation: configurationstore.OperationSet, Path: []string{"provider", "codex", "executable"}, Value: `C:\Tools\codex.exe`}
	preview, err := store.Preview(ctx, configurationstore.TargetUser, mutation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(user); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview wrote file: %v", err)
	}
	written, err := store.Apply(ctx, configurationstore.TargetUser, mutation, initial.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if written.Revision != preview.Revision {
		t.Fatalf("written revision %s != preview %s", written.Revision, preview.Revision)
	}
	if _, err := store.Apply(ctx, configurationstore.TargetUser, mutation, initial.Revision); !errors.Is(err, configurationstore.ErrRevisionConflict) {
		t.Fatalf("stale apply error = %v", err)
	}
	restored, err := store.Restore(ctx, configurationstore.TargetUser, written.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Present || restored.Revision != initial.Revision {
		t.Fatalf("restore = %#v, initial = %#v", restored, initial)
	}
	if _, err := os.Stat(user); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent restore left file: %v", err)
	}
}

func TestMutationPreservesCommentsAndUnknownKeys(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	user := filepath.Join(root, "config.yaml")
	original := []byte("# keep this comment\nunknown:\n  nested: yes\nprovider:\n  codex:\n    executable: old\n")
	if err := os.WriteFile(user, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := mustStore(t, user, filepath.Join(root, "project.yaml"), filepath.Join(root, "secrets.yaml"), filepath.Join(root, "data"))
	snapshot, _ := store.Snapshot(context.Background(), configurationstore.TargetUser)
	written, err := store.Apply(context.Background(), configurationstore.TargetUser, configurationstore.Mutation{Operation: configurationstore.OperationSet, Path: []string{"provider", "codex", "executable"}, Value: "new"}, snapshot.Revision)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "# keep this comment") || !strings.Contains(text, "unknown:") || !strings.Contains(text, "nested: yes") || !strings.Contains(text, "executable: new") {
		t.Fatalf("mutated YAML lost content:\n%s", text)
	}
	restored, err := store.Restore(context.Background(), configurationstore.TargetUser, written.Revision)
	if err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(original) || restored.Revision != snapshot.Revision {
		t.Fatalf("restore did not reproduce exact bytes\nwant %q\ngot  %q", original, content)
	}
}

func TestConcurrentCASHasOneWinnerAndSecretsStaySeparate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	user := filepath.Join(root, "config.yaml")
	secret := filepath.Join(root, "secrets.yaml")
	store := mustStore(t, user, filepath.Join(root, "project.yaml"), secret, filepath.Join(root, "data"))
	snapshot, _ := store.Snapshot(context.Background(), configurationstore.TargetUser)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, value := range []string{"a", "b"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			_, err := store.Apply(context.Background(), configurationstore.TargetUser, configurationstore.Mutation{Operation: configurationstore.OperationSet, Path: []string{"x"}, Value: value}, snapshot.Revision)
			results <- err
		}(value)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, configurationstore.ErrRevisionConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	secretRevision, err := store.SecretRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	canary := "DAR130-SECRET-CANARY"
	receipt, err := store.PutSecret(context.Background(), "codex-api-key", canary, secretRevision)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Revision == secretRevision {
		t.Fatal("secret revision did not change")
	}
	ordinary, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ordinary), canary) {
		t.Fatal("secret leaked into ordinary configuration")
	}
	secretBytes, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secretBytes), canary) {
		t.Fatal("secret file omitted secret")
	}
}

func TestRecoversInterruptedReplacementAndRejectsOversize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	user := filepath.Join(root, "config.yaml")
	staged := user + ".replacing"
	if err := os.WriteFile(staged, []byte("kept: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := mustStore(t, user, filepath.Join(root, "project.yaml"), filepath.Join(root, "secrets.yaml"), filepath.Join(root, "data"))
	snapshot, err := store.Snapshot(context.Background(), configurationstore.TargetUser)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Present || snapshot.Values["kept"] != true {
		t.Fatalf("recovered snapshot = %#v", snapshot)
	}
	if err := os.WriteFile(user, []byte(strings.Repeat("x", configfiles.MaxFileSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Snapshot(context.Background(), configurationstore.TargetUser); err == nil {
		t.Fatal("oversized configuration was accepted")
	}
}

func TestRejectsLinkedConfigurationBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Mkdir(realDirectory, 0o700); err != nil { t.Fatal(err) }
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil { t.Skipf("directory links unavailable: %v", err) }
	store := mustStore(t, filepath.Join(linkedDirectory, "config.yaml"), filepath.Join(root, "project.yaml"), filepath.Join(root, "secrets.yaml"), filepath.Join(root, "data"))
	if _, err := store.Snapshot(context.Background(), configurationstore.TargetUser); !errors.Is(err, configurationstore.ErrPathBoundary) {
		t.Fatalf("linked boundary error = %v", err)
	}
}

func mustStore(t *testing.T, user, project, secrets, data string) *filesystem.Store {
	t.Helper()
	store, err := filesystem.New(configfiles.FileLocations{UserConfig: user, ProjectConfig: project, UserSecrets: secrets}, data)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
