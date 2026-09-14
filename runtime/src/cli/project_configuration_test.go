package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"darkstar/src/core/configmutation"
	platformport "darkstar/src/ports/platform"
)

func TestPlanningProjectConfigurationUsesItsOwnFileThroughDaemon(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalResolver := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) {
		return paths, nil
	}
	t.Cleanup(func() {
		resolveApplicationPaths = originalResolver
	})
	service := startAcceptanceService(t, paths, "45454545454545454545454545454545")
	t.Cleanup(func() {
		_ = service.Close()
	})
	project := createPlanningCLIProject(t, "Configuration product", "configuration-product")
	var state configmutation.State
	runCLIJSON(t, []string{"configuration", "state", "--project", project.ProjectID, "--json"}, &state)
	var result configmutation.ApplyResult
	runCLIJSON(t, []string{"configuration", "set", "--project", project.ProjectID, "--key", "workspace.baseRef", "--value-type", "string", "--value", "product/main", "--revision", state.Revision, "--idempotency-key", "product-config-base", "--json"}, &result)
	if result.State.Scope.ProjectID() != project.ProjectID || len(result.State.Configured) != 1 || result.State.Configured[0].Value.Value() != "product/main" {
		t.Fatalf("project configuration result = %#v", result)
	}
	path := filepath.Join(paths.Data, "projects", project.ProjectID, "configuration", "config.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("project configuration file was not created", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".darkstar", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("planning project mutated daemon-root configuration: %v", err)
	}
}
