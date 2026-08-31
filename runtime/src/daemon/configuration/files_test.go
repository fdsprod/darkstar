package configuration_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/core/config"
	"github.com/fdsprod/darkstar/runtime/src/daemon/configuration"
	"github.com/fdsprod/darkstar/runtime/src/ports/platform"
)

func TestResolveFileLocationsSeparatesProjectAndUserOnlyFiles(t *testing.T) {
	t.Parallel()

	localRoot := filepath.Join(t.TempDir(), "LocalAppData", "DARKSTAR")
	projectRoot := filepath.Join(t.TempDir(), "project")
	locations, err := configuration.ResolveFileLocations(platform.Paths{Config: filepath.Join(localRoot, "config")}, projectRoot)
	if err != nil {
		t.Fatalf("ResolveFileLocations() error = %v", err)
	}
	if got, want := locations.UserConfig, filepath.Join(localRoot, "config", "config.yaml"); got != want {
		t.Fatalf("UserConfig = %q, want %q", got, want)
	}
	if got, want := locations.UserSecrets, filepath.Join(localRoot, "config", "secrets.yaml"); got != want {
		t.Fatalf("UserSecrets = %q, want %q", got, want)
	}
	if strings.HasPrefix(strings.ToLower(locations.UserSecrets), strings.ToLower(projectRoot)+string(filepath.Separator)) {
		t.Fatalf("UserSecrets %q must not be inside project root %q", locations.UserSecrets, projectRoot)
	}
	if got, want := locations.ProjectConfig, filepath.Join(projectRoot, ".darkstar", "config.yaml"); got != want {
		t.Fatalf("ProjectConfig = %q, want %q", got, want)
	}
}

func TestResolveLoadsUserThenProjectAndAppliesOverrides(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	locations := configuration.FileLocations{
		UserConfig:    filepath.Join(root, "user.yaml"),
		UserSecrets:   filepath.Join(root, "secrets.yaml"),
		ProjectConfig: filepath.Join(root, "project.yaml"),
	}
	writeFile(t, locations.UserConfig, "spec:\n  provider:\n    default: codex-personal\n    timeout: 30\n")
	writeFile(t, locations.ProjectConfig, "spec:\n  provider:\n    timeout: 60\n")
	// A secrets file exists, but ordinary configuration resolution never reads it.
	writeFile(t, locations.UserSecrets, "token: do-not-load\n")

	defaults := mustDefaults(t, map[string]any{
		"spec": map[string]any{"provider": map[string]any{"default": "fake", "timeout": 10}},
	})
	commandLine := mustCLI(t, map[string]any{
		"spec": map[string]any{"provider": map[string]any{"timeout": 5}},
	})
	effective, err := configuration.Resolve(defaults, locations, commandLine)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	assertValue(t, effective, []string{"spec", "provider", "default"}, "codex-personal", config.ScopeUser)
	assertValue(t, effective, []string{"spec", "provider", "timeout"}, 5, config.ScopeCLI)
	if _, ok := effective.Lookup("token"); ok {
		t.Fatal("Resolve() loaded the separate secrets file into ordinary configuration")
	}
}

func TestLoadOptionalFileTreatsMissingAsAbsent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.yaml")
	if _, found, err := configuration.LoadOptionalFile(config.ScopeUser, path); err != nil || found {
		t.Fatalf("LoadOptionalFile(missing) = found %v, error %v; want false, nil", found, err)
	}
}

func TestLoadOptionalFileRejectsUnsafeDocuments(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"multiple documents": "value: one\n---\nvalue: two\n",
		"duplicate keys":     "value: one\nvalue: two\n",
		"non-mapping root":   "- one\n- two\n",
	}
	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, content)
			if _, _, err := configuration.LoadOptionalFile(config.ScopeProject, path); err == nil {
				t.Fatal("LoadOptionalFile() error = nil, want parse error")
			}
		})
	}
}

func TestLoadOptionalFileRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "value: "+strings.Repeat("x", configuration.MaxFileSize))
	if _, _, err := configuration.LoadOptionalFile(config.ScopeProject, path); err == nil {
		t.Fatal("LoadOptionalFile() error = nil, want size error")
	}
}

func assertValue(t *testing.T, effective config.Effective, path []string, want any, scope config.Scope) {
	t.Helper()
	resolved, ok := effective.Lookup(path...)
	if !ok {
		t.Fatalf("Lookup(%v) not found", path)
	}
	if got := resolved.Value(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup(%v).Value() = %#v, want %#v", path, got, want)
	}
	if got := resolved.Source().Scope(); got != scope {
		t.Fatalf("Lookup(%v).Source().Scope() = %s, want %s", path, got, scope)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustLayer(t *testing.T, layer config.Layer, err error) config.Layer {
	t.Helper()
	if err != nil {
		t.Fatalf("create layer: %v", err)
	}
	return layer
}

func mustDefaults(t *testing.T, values map[string]any) config.Layer {
	t.Helper()
	layer, err := config.Defaults(values)
	return mustLayer(t, layer, err)
}

func mustCLI(t *testing.T, values map[string]any) config.Layer {
	t.Helper()
	layer, err := config.CLIOverride(values)
	return mustLayer(t, layer, err)
}
