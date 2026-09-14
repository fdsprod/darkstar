package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/core/investigation"
)

func TestInvestigationCLIUsesExactInputsAndExplicitMutationRevisions(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "request.json")
	request := investigation.PrepareRequest{ScopeID: "scope_" + strings.Repeat("0", 26), Task: investigation.TaskInput{Kind: "feature_brief", FeatureBrief: &investigation.ArtifactReference{ArtifactID: "artifact_brief", Version: 2, SHA256: strings.Repeat("a", 64)}}, Concurrency: 3}
	data, _ := json.Marshal(request)
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseInvestigationCommand([]string{"prepare", filename, "--idempotency-key", "prepare-brief-key"})
	if err != nil || parsed.method != http.MethodPost || parsed.resource != "investigations" || parsed.key != "prepare-brief-key" {
		t.Fatalf("prepare command: %#v %v", parsed, err)
	}
	actual := parsed.body.(investigation.PrepareRequest)
	if actual.Task.FeatureBrief.SHA256 != request.Task.FeatureBrief.SHA256 || actual.Task.FeatureBrief.Version != 2 || actual.Concurrency != 3 {
		t.Fatal("CLI changed the immutable source request")
	}
	id := "investigation_" + strings.Repeat("0", 26)
	for _, action := range []string{"start", "retry", "cancel"} {
		parsed, err = parseInvestigationCommand([]string{action, id, "--revision", "7", "--idempotency-key", "command-key"})
		if err != nil || parsed.resource != "investigations/"+id+"/"+action || parsed.revision != 7 || parsed.key != "command-key" {
			t.Fatalf("%s command: %#v %v", action, parsed, err)
		}
	}
	parsed, err = parseInvestigationCommand([]string{"show", id})
	if err != nil || parsed.method != http.MethodGet || parsed.body != nil || parsed.key != "" {
		t.Fatalf("show command: %#v %v", parsed, err)
	}
}

func TestInvestigationCLIRejectsAmbiguousOrAuthorityBearingInput(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "request.json")
	for _, body := range []string{
		`null`,
		`{"scopeId":"scope_00000000000000000000000000","task":{"kind":"text","text":""}}`,
		`{"scopeId":"scope_00000000000000000000000000","task":{"kind":"feature_brief","featureBrief":{"artifactId":"artifact_brief","version":1}}}`,
		`{"scopeId":"scope_00000000000000000000000000","task":{"kind":"text","text":"Inspect","featureBrief":{}}}`,
		`{"scopeId":"scope_00000000000000000000000000","task":{"kind":"text","text":"Inspect"},"provider":"unsafe"}`,
		`{"scopeId":"scope_00000000000000000000000000","task":{"kind":"text","text":"Inspect"},"concurrency":9}`,
		`{} {}`,
	} {
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := parseInvestigationCommand([]string{"prepare", filename}); err == nil {
			t.Fatalf("accepted invalid prepare request: %s", body)
		}
	}
	id := "investigation_" + strings.Repeat("0", 26)
	for _, args := range [][]string{
		{"start", id},
		{"retry", id, "--revision", "0"},
		{"cancel", id, "--revision", "1", "--revision", "2"},
		{"start", id, "--revision", "1", "--idempotency-key", ""},
		{"show", id, "--idempotency-key", "read-mutation"},
		{"start", "../another", "--revision", "1"},
		{"submit", id, "--revision", "1"},
	} {
		if _, err := parseInvestigationCommand(args); err == nil {
			t.Fatalf("accepted invalid command %v", args)
		}
	}
}
