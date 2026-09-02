package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/ports/platform"
	"darkstar/src/ports/workflowstore"
)

func TestResolveDirectoriesUsesPlatformAndProjectScopes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directories, err := ResolveDirectories(filepath.Join(root, "shipped"), platform.Paths{Config: filepath.Join(root, "config")}, filepath.Join(root, "project"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Directory{
		{Scope: workflowstore.ScopeDefault, Path: filepath.Join(root, "shipped")},
		{Scope: workflowstore.ScopeUser, Path: filepath.Join(root, "config", "workflows")},
		{Scope: workflowstore.ScopeProject, Path: filepath.Join(root, "project", ".darkstar", "workflows")},
	}
	for index := range want {
		if directories[index] != want[index] {
			t.Fatalf("directory %d = %#v, want %#v", index, directories[index], want[index])
		}
	}
}

func TestSourceLoadsConfiguredScopesInStableOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	defaults := filepath.Join(root, "defaults")
	projects := filepath.Join(root, "project")
	for _, directory := range []string{defaults, projects} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkflowFile(t, filepath.Join(defaults, "delivery.json"), workflowJSON("delivery", "1.0.0", "default"))
	writeWorkflowFile(t, filepath.Join(projects, "delivery.yaml"), workflowYAML("delivery", "1.0.0", "project"))
	writeWorkflowFile(t, filepath.Join(projects, "ignored.txt"), "not a workflow")

	source, err := New(
		Directory{Scope: workflowstore.ScopeProject, Path: projects},
		Directory{Scope: workflowstore.ScopeUser, Path: filepath.Join(root, "missing-user")},
		Directory{Scope: workflowstore.ScopeDefault, Path: defaults},
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := source.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Scope != workflowstore.ScopeDefault || candidates[1].Scope != workflowstore.ScopeProject {
		t.Fatalf("candidates = %#v, want default then project", candidates)
	}
}

func TestSourceRejectsUnsafeYAML(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"duplicate keys":     "apiVersion: one\napiVersion: two\n",
		"multiple documents": "apiVersion: one\n---\napiVersion: two\n",
		"timestamp":          "created: 2026-09-01T00:00:00Z\n",
		"alias":              "base: &base value\ncopy: *base\n",
	}
	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			writeWorkflowFile(t, filepath.Join(directory, "unsafe.yaml"), content)
			source, err := New(Directory{Scope: workflowstore.ScopeProject, Path: directory})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Load(context.Background()); err == nil {
				t.Fatal("Load() error = nil, want unsafe YAML rejection")
			}
		})
	}
}

func writeWorkflowFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func workflowJSON(name, version, description string) string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"` + name + `","version":"` + version + `","description":"` + description + `"},"spec":{"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}

func workflowYAML(name, version, description string) string {
	return strings.TrimSpace(`
apiVersion: darkstar.local/v1alpha1
kind: Workflow
metadata:
  name: `+name+`
  version: `+version+`
  description: `+description+`
spec:
  routeDefaults:
    entry: finish
    terminals: [finish]
  nodes:
    finish:
      type: reasoning
      entry: true
      terminal: true
      inputs: {}
      outputs: {}
      reasoning:
        agent: fake
      checkpoint:
        mode: none
      transitions: []
`) + "\n"
}
