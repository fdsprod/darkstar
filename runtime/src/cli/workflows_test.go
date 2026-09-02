package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/core/workflow"
	platformport "github.com/fdsprod/darkstar/runtime/src/ports/platform"
)

func TestWorkflowCLIInstallListGraphAndPreviewJSON(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalResolver := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) { return paths, nil }
	t.Cleanup(func() { resolveApplicationPaths = originalResolver })
	service := startAcceptanceService(t, paths, "33333333333333333333333333333333")
	t.Cleanup(func() { _ = service.Close() })

	definitionPath := filepath.Join(root, "workflow.json")
	if err := os.WriteFile(definitionPath, []byte(cliWorkflowDocument()), 0o600); err != nil {
		t.Fatal(err)
	}
	var installed workflow.InstallResult
	runCLIJSON(t, []string{"workflow", "install", definitionPath, "--json"}, &struct {
		SchemaVersion int                     `json:"schemaVersion"`
		Result        *workflow.InstallResult `json:"result"`
	}{Result: &installed})
	if installed.Version.Name != "cli-workflow" || installed.Disposition != workflow.InstallCreated {
		t.Fatalf("install = %#v", installed)
	}

	var listed []workflow.VersionSummary
	runCLIJSON(t, []string{"workflow", "list", "--json"}, &struct {
		SchemaVersion int                        `json:"schemaVersion"`
		Result        *[]workflow.VersionSummary `json:"result"`
	}{Result: &listed})
	if len(listed) != 1 || listed[0].Name != "cli-workflow" {
		t.Fatalf("list = %#v", listed)
	}
	var shown workflow.Definition
	runCLIJSON(t, []string{"workflow", "show", "cli-workflow", "--json"}, &struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Result        *workflow.Definition `json:"result"`
	}{Result: &shown})
	if shown.Version.Name != "cli-workflow" || shown.Document.Metadata.Version != "1.0.0" {
		t.Fatalf("show = %#v", shown)
	}

	var graph workflow.Graph
	runCLIJSON(t, []string{"workflow", "graph", "cli-workflow", "--json"}, &struct {
		SchemaVersion int             `json:"schemaVersion"`
		Result        *workflow.Graph `json:"result"`
	}{Result: &graph})
	if len(graph.Nodes) != 1 || graph.Nodes[0].ID != "finish" {
		t.Fatalf("graph = %#v", graph)
	}

	var preview workflow.RoutePreview
	runCLIJSON(t, []string{"workflow", "preview", "cli-workflow", "--json"}, &struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Result        *workflow.RoutePreview `json:"result"`
	}{Result: &preview})
	if preview.Route.Entry != "finish" || len(preview.Route.Nodes) != 1 {
		t.Fatalf("preview = %#v", preview)
	}

	invalidPath := filepath.Join(root, "invalid.json")
	invalid := strings.Replace(cliWorkflowDocument(), `"finish":{"type"`, `"orphan":{"type":"reasoning","inputs":{},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]},"finish":{"type"`, 1)
	if err := os.WriteFile(invalidPath, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"workflow", "validate", invalidPath, "--json"}, &stdout, &stderr); code != int(ExitValidationFailed) {
		t.Fatalf("validate code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var validation struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Result        workflow.ValidationReport `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &validation); err != nil || len(validation.Result.Issues) == 0 {
		t.Fatalf("validation output=%s error=%v", stdout.String(), err)
	}
}

func TestWorkflowValidationFindingsUseValidationExitAndMachineEnvelope(t *testing.T) {
	input := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(input, []byte(strings.Replace(cliWorkflowDocument(), `"finish":{"type"`, `"orphan":{"type":"reasoning","inputs":{},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]},"finish":{"type"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	// File normalization happens before connection; this test covers the stable local parser.
	candidate, err := readWorkflowCandidate(input)
	if err != nil || len(candidate.Document) == 0 {
		t.Fatalf("candidate = %#v, %v", candidate, err)
	}
	var output bytes.Buffer
	if code := writeWorkflowResult(workflow.ValidationReport{Issues: workflow.ValidationErrors{{Code: workflow.ValidationUnreachableNode, Message: "unreachable"}}}, "invalid", true, true, &output, &bytes.Buffer{}, "darkstar workflow validate"); code != int(ExitValidationFailed) {
		t.Fatalf("code = %d", code)
	}
	var envelope struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Result        workflow.ValidationReport `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || len(envelope.Result.Issues) != 1 {
		t.Fatalf("output = %s, %v", output.String(), err)
	}
}

func cliWorkflowDocument() string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"cli-workflow","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}
