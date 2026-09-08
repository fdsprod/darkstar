package workflow_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/workflow"
)

func TestNodeDefinitionLibraryKeepsVersionsImmutableAndBuiltInsDerivativeOnly(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	builtin := nodeDefinition(workflow.BuiltInNodeDefinitionRef{Name: "route/assessment", Version: "1.0.0"}, workflow.NodeImplementationRouting, now)
	library, err := workflow.NewNodeDefinitionLibrary(builtin)
	if err != nil {
		t.Fatal(err)
	}
	stored := library.Search(workflow.NodeDefinitionFilter{})[0]
	if _, err := library.Publish(builtin); !errors.Is(err, workflow.ErrNodeDefinitionImmutable) {
		t.Fatalf("built-in publish error = %v", err)
	}
	derived, err := library.Duplicate(stored.Ref, workflow.ProjectNodeDefinitionRef{ProjectID: "project-1", Name: "route/custom", Version: "1.0.0"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if derived.DerivedFrom == nil || derived.DerivedFrom.Digest != stored.Ref.Digest {
		t.Fatalf("derivative provenance = %#v", derived.DerivedFrom)
	}
	versioned, err := library.Version(derived.Ref, "1.1.0", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if versioned.Ref.Ref.DefinitionVersion() != "1.1.0" || versioned.Ref.Digest == derived.Ref.Digest {
		t.Fatalf("versioned definition = %#v", versioned.Ref)
	}
	archived, err := library.Archive(derived.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Lifecycle != workflow.NodeDefinitionArchived {
		t.Fatalf("lifecycle = %s", archived.Lifecycle)
	}
	resolved, err := library.Resolve(derived.Ref)
	if err != nil || resolved.Ref.Digest != derived.Ref.Digest {
		t.Fatalf("historical exact resolve = %#v, %v", resolved, err)
	}
	active := workflow.NodeDefinitionActive
	if got := library.Search(workflow.NodeDefinitionFilter{Query: "custom", Lifecycle: &active}); len(got) != 1 || got[0].Ref.Ref.DefinitionVersion() != "1.1.0" {
		t.Fatalf("active search did not isolate the current version: %#v", got)
	}
}

func TestRoutingNodeAcceptsOnlyDeclaredBranchesAndTypedOutputs(t *testing.T) {
	document := routingDocument("to_assessment")
	if _, err := workflow.Decode([]byte(strings.Replace(document, workflow.APIVersionV1Alpha3, workflow.APIVersionV1Alpha2, 1))); err == nil || !strings.Contains(err.Error(), "v1alpha3") {
		t.Fatalf("v1alpha2 routing error = %v", err)
	}
	parsed, canonical, _, err := workflow.Canonicalize([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if issues := workflow.Validate(parsed); len(issues) != 0 {
		t.Fatalf("valid routing node issues = %#v", issues)
	}
	var wire map[string]any
	if err := json.Unmarshal(canonical, &wire); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(document, `"transition":"to_assessment"`, `"transition":"invented_edge"`, 1)
	parsed, err = workflow.Decode([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	issues := workflow.Validate(parsed)
	if len(issues) == 0 || issues[0].Code != workflow.ValidationRoutingInvalid {
		t.Fatalf("undeclared branch issues = %#v", issues)
	}
}

func nodeDefinition(ref workflow.NodeDefinitionRef, kind workflow.NodeImplementationKind, now time.Time) workflow.NodeDefinition {
	return workflow.NodeDefinition{Ref: workflow.ResolvedNodeDefinitionRef{Ref: ref}, DisplayName: "Route assessment", Description: "Select safe route", Compatibility: "darkstar.local/v1alpha2", Inputs: map[workflow.Identifier]workflow.ValueDeclaration{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, ConfigurationSchema: json.RawMessage(`{"type":"object"}`), Implementation: kind, RequiredCapabilities: []workflow.CapabilityReference{}, Lifecycle: workflow.NodeDefinitionActive, CreatedAt: now}
}

func routingDocument(branchTransition string) string {
	return `{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"router","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"route","terminals":["assessment"]},"nodes":{"route":{"type":"routing","entry":true,"terminal":false,"inputs":{},"outputs":{"selected_route":{"type":"string"},"rationale":{"type":"string"},"advice":{"type":"string"},"missing_information":{"type":"array"},"assumptions":{"type":"array"},"confirmation_required":{"type":"boolean"}},"routing":{"agent":"router","branches":[{"name":"assessment","transition":"` + branchTransition + `"}],"routeOutput":"selected_route","rationaleOutput":"rationale","adviceOutput":"advice","missingInformationOutput":"missing_information","assumptionsOutput":"assumptions","confirmationOutput":"confirmation_required"},"checkpoint":{"mode":"none"},"transitions":[{"id":"to_assessment","to":"assessment"}]},"assessment":{"type":"command","entry":false,"terminal":true,"inputs":{},"outputs":{},"command":{"argv":["assess"]},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}
