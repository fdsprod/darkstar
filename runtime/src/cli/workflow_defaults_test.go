package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"darkstar/src/adapters/statestore/sqlite"
	workflowfilesystem "darkstar/src/adapters/workflowstore/filesystem"
	"darkstar/src/core/workflow"
	platformport "darkstar/src/ports/platform"
	"darkstar/src/ports/workflowstore"
)

func TestConfiguredWorkflowsIncludeAndInstallShippedDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	defaults := filepath.Join(root, "workflows")
	if err := os.Mkdir(defaults, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"software-delivery.json", "story-execution.json"} {
		content, err := os.ReadFile(filepath.Join(repositoryRootForWorkflowTest(t), "examples", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(defaults, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	service := daemonAPIService{
		paths:                    platformport.Paths{Config: filepath.Join(root, "config")},
		projectRoot:              filepath.Join(root, "project"),
		defaultWorkflowDirectory: defaults,
	}
	directories, err := service.workflowDirectories()
	if err != nil {
		t.Fatal(err)
	}
	source, err := workflowfilesystem.New(directories...)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(context.Background(), filepath.Join(root, "darkstar.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	catalog, err := workflow.NewCatalog(source, database)
	if err != nil {
		t.Fatal(err)
	}
	results, err := catalog.InstallConfigured(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("installed workflows = %d, want 2", len(results))
	}
	for _, name := range []string{"darkstar/software-delivery", "darkstar/story-execution"} {
		definition, err := catalog.Definition(context.Background(), name, "1.0.0")
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if definition.Version.SourceScope != workflowstore.ScopeDefault {
			t.Fatalf("%s source = %q, want default", name, definition.Version.SourceScope)
		}
	}
}

func repositoryRootForWorkflowTest(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test filename")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}
