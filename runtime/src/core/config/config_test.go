package config_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"darkstar/src/core/config"
)

func TestResolveUsesCanonicalPrecedenceAndAttributesEveryLeaf(t *testing.T) {
	t.Parallel()

	defaults := mustDefaults(t, map[string]any{
		"spec": map[string]any{
			"provider":  map[string]any{"default": "fake", "timeout": 30},
			"artifacts": map[string]any{"root": ".darkstar/artifacts"},
		},
	})
	userPath := filepath.Join(t.TempDir(), "user.yaml")
	projectPath := filepath.Join(t.TempDir(), "project.yaml")
	user := mustUser(t, userPath, map[string]any{
		"spec": map[string]any{"provider": map[string]any{"default": "codex-personal"}},
	})
	project := mustProject(t, projectPath, map[string]any{
		"spec": map[string]any{"provider": map[string]any{"timeout": 60}},
	})
	run := mustRun(t, "work:DAR-22", map[string]any{
		"spec": map[string]any{"artifacts": map[string]any{"root": "run-artifacts"}},
	})
	commandLine := mustCLI(t, map[string]any{
		"spec": map[string]any{"provider": map[string]any{"timeout": 5}},
	})

	// Deliberately provide overlays out of order; scope, not call order, owns precedence.
	effective, err := config.Resolve(defaults, commandLine, project, user, run)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	assertResolved(t, effective, []string{"spec", "provider", "default"}, "codex-personal", config.ScopeUser, userPath)
	assertResolved(t, effective, []string{"spec", "provider", "timeout"}, 5, config.ScopeCLI, "command line")
	assertResolved(t, effective, []string{"spec", "artifacts", "root"}, "run-artifacts", config.ScopeRun, "work:DAR-22")

	sources := effective.Sources()
	for _, pointer := range []string{
		"/spec/provider/default",
		"/spec/provider/timeout",
		"/spec/artifacts/root",
	} {
		if _, ok := sources[pointer]; !ok {
			t.Errorf("Sources() missing %q: %#v", pointer, sources)
		}
	}
}

func TestResolveReplacesCollectionsWithoutStaleAttribution(t *testing.T) {
	t.Parallel()

	defaults := mustDefaults(t, map[string]any{
		"provider": map[string]any{"name": "fake", "options": map[string]any{"a": 1}},
		"checks":   []any{"test", "lint"},
	})
	projectPath := filepath.Join(t.TempDir(), "project.yaml")
	project := mustProject(t, projectPath, map[string]any{
		"provider": "disabled",
		"checks":   []any{"test"},
	})

	effective, err := config.Resolve(defaults, project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, exists := effective.Sources()["/provider/name"]; exists {
		t.Fatal("Sources() retained an attribution below a replaced object")
	}
	assertResolved(t, effective, []string{"provider"}, "disabled", config.ScopeProject, projectPath)
	assertResolved(t, effective, []string{"checks"}, []any{"test"}, config.ScopeProject, projectPath)
}

func TestEffectiveReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()

	input := map[string]any{"nested": map[string]any{"value": "original"}}
	defaults := mustDefaults(t, input)
	input["nested"].(map[string]any)["value"] = "mutated input"
	effective, err := config.Resolve(defaults)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	values := effective.Values()
	values["nested"].(map[string]any)["value"] = "mutated output"
	assertResolved(t, effective, []string{"nested", "value"}, "original", config.ScopeDefault, "shipped defaults")
}

func TestResolveRejectsDuplicateScopes(t *testing.T) {
	t.Parallel()

	defaults := mustDefaults(t, nil)
	first := mustUser(t, filepath.Join(t.TempDir(), "one.yaml"), nil)
	second := mustUser(t, filepath.Join(t.TempDir(), "two.yaml"), nil)
	if _, err := config.Resolve(defaults, first, second); err == nil {
		t.Fatal("Resolve() error = nil, want duplicate-scope error")
	}
}

func TestSourceMarshalsAsStablePublicShape(t *testing.T) {
	t.Parallel()

	layer := mustCLI(t, nil)
	encoded, err := json.Marshal(layer.Source())
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if got, want := string(encoded), `{"scope":"cli","reference":"command line"}`; got != want {
		t.Fatalf("json.Marshal() = %s, want %s", got, want)
	}
}

func TestEffectiveReportSortsEntriesAndPreservesSafeDisplays(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	userPath := filepath.Join(root, "config", "config.yaml")
	projectPath := filepath.Join(root, "project", ".darkstar", "config.yaml")
	defaults := mustDefaults(t, map[string]any{
		"zeta":   true,
		"count":  12,
		"nil":    nil,
		"nested": map[string]any{"safe": "literal value"},
		"array":  []any{"one", 2},
	})
	user := mustUser(t, userPath, map[string]any{"zeta": false})
	effective, err := config.Resolve(defaults, user)
	if err != nil {
		t.Fatal(err)
	}
	report, err := config.NewEffectiveReport(filepath.Join(root, "project"), []config.File{
		{Scope: config.FileScopeUser, Path: userPath},
		{Scope: config.FileScopeProject, Path: projectPath},
	}, effective)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.ProjectRoot != filepath.Join(root, "project") || len(report.Files) != 2 {
		t.Fatalf("report identity = %#v", report)
	}
	want := []struct {
		path    string
		kind    config.ValueKind
		display string
	}{
		{path: "/array", kind: config.ValueJSON, display: `["one",2]`},
		{path: "/count", kind: config.ValueNumber, display: "12"},
		{path: "/nested/safe", kind: config.ValueString, display: "literal value"},
		{path: "/nil", kind: config.ValueNull, display: "null"},
		{path: "/zeta", kind: config.ValueBoolean, display: "false"},
	}
	if len(report.Entries) != len(want) {
		t.Fatalf("entries = %#v", report.Entries)
	}
	for index, expected := range want {
		entry := report.Entries[index]
		if entry.Path != expected.path || entry.Value.Kind != expected.kind || entry.Value.Display != expected.display {
			t.Fatalf("entry %d = %#v, want %#v", index, entry, expected)
		}
	}
	if report.Entries[4].Source.Scope() != config.ScopeUser || report.Entries[4].Source.Reference() != userPath {
		t.Fatalf("winning source = %#v", report.Entries[4].Source)
	}
}

func TestEffectiveReportRedactsSecretPathsAndSecretLikeValues(t *testing.T) {
	t.Parallel()
	secret := "never-emit-this-value"
	awsSecret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	clientSecret := "opaque-client-secret-value"
	defaults := mustDefaults(t, map[string]any{
		"apiToken":           secret,
		"awsSecretAccessKey": awsSecret,
		"clientSecretValue":  clientSecret,
		"safeName":           "Authorization: Bearer " + secret,
		"nested":             []any{map[string]any{"password": secret}},
		"ordinary":           "visible",
	})
	effective, err := config.Resolve(defaults)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	report, err := config.NewEffectiveReport(root, []config.File{
		{Scope: config.FileScopeUser, Path: filepath.Join(root, "config.yaml")},
		{Scope: config.FileScopeProject, Path: filepath.Join(root, ".darkstar", "config.yaml")},
	}, effective)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), awsSecret) || strings.Contains(string(encoded), clientSecret) {
		t.Fatalf("report disclosed secret: %s", encoded)
	}
	redacted := 0
	for _, entry := range report.Entries {
		if entry.Value.Kind == config.ValueRedacted && entry.Value.Display == "[redacted]" {
			redacted++
		}
	}
	if redacted != 5 {
		t.Fatalf("redacted entries = %d, report = %#v", redacted, report)
	}
}

func TestEffectiveReportRejectsSecretFileScope(t *testing.T) {
	t.Parallel()
	defaults := mustDefaults(t, nil)
	effective, err := config.Resolve(defaults)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := config.NewEffectiveReport(root, []config.File{
		{Scope: config.FileScope("secret"), Path: filepath.Join(root, "secrets.yaml")},
		{Scope: config.FileScopeProject, Path: filepath.Join(root, ".darkstar", "config.yaml")},
	}, effective); err == nil {
		t.Fatal("NewEffectiveReport() accepted a secret file descriptor")
	}
}

func assertResolved(t *testing.T, effective config.Effective, path []string, want any, scope config.Scope, reference string) {
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
	if got := resolved.Source().Reference(); got != reference {
		t.Fatalf("Lookup(%v).Source().Reference() = %q, want %q", path, got, reference)
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

func mustUser(t *testing.T, path string, values map[string]any) config.Layer {
	t.Helper()
	layer, err := config.UserFile(path, values)
	return mustLayer(t, layer, err)
}

func mustProject(t *testing.T, path string, values map[string]any) config.Layer {
	t.Helper()
	layer, err := config.ProjectFile(path, values)
	return mustLayer(t, layer, err)
}

func mustRun(t *testing.T, reference string, values map[string]any) config.Layer {
	t.Helper()
	layer, err := config.RunOverride(reference, values)
	return mustLayer(t, layer, err)
}

func mustCLI(t *testing.T, values map[string]any) config.Layer {
	t.Helper()
	layer, err := config.CLIOverride(values)
	return mustLayer(t, layer, err)
}
