package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"darkstar/src/core/repositoryscope"
)

func TestRepositoryScopeCLIRequiresExplicitSelections(t *testing.T) {
	const project = "project_01K00000000000000000000001"
	filename := filepath.Join(t.TempDir(), "repositories.json")
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"none", `[]`, true},
		{"selected", `[{"repositoryId":"repository_01K00000000000000000000001","ref":"refs/heads/main"}]`, true},
		{"null", `null`, false},
		{"unknown", `[{"repositoryId":"repo","ref":"main","path":"/ambient"}]`, false},
		{"trailing", `[] []`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filename, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			method, resource, body, key, err := parseRepositoryScopeCommand([]string{"prepare", project, "--repositories-file", filename, "--idempotency-key", "scope-command-key"})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			request := body.(repositoryscope.PrepareRequest)
			if method != http.MethodPost || resource != "investigation-scopes" || key != "scope-command-key" || request.ProjectID != project || request.Repositories == nil {
				t.Fatalf("scope command lost explicit identity or selections: %#v", body)
			}
		})
	}
	for _, args := range [][]string{
		{"prepare", project},
		{"prepare", project, "--all", "true"},
		{"prepare", project, "--repositories-file", filename, "--repositories-file", filename},
		{"show", "../another-scope"},
	} {
		if _, _, _, _, err := parseRepositoryScopeCommand(args); err == nil {
			t.Fatalf("accepted invalid scope command: %v", args)
		}
	}
	const scope = "scope_01K00000000000000000000001"
	method, resource, body, key, err := parseRepositoryScopeCommand([]string{"show", scope})
	if err != nil || method != http.MethodGet || resource != "investigation-scopes/"+scope || body != nil || key != "" {
		t.Fatalf("show command = %s %s %#v %s %v", method, resource, body, key, err)
	}
}
