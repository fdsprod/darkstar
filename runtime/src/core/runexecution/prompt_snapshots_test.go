package runexecution

import (
	"context"
	"errors"
	"testing"

	"darkstar/src/core/contentlibrary"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/contentstore"
	"darkstar/src/ports/statestore"
)

type contentPlanner struct {
	checkpointPlanner
	version contentstore.Version
	err     error
}

func (p contentPlanner) ResolveContent(context.Context, contentstore.Reference) (contentstore.Version, error) {
	return p.version, p.err
}

func TestPromptSnapshotResolutionFailsClosedAndCopiesPublishedVersion(t *testing.T) {
	version := contentlibrary.BuiltinItems()[0].Versions[0]
	document := workflow.Document{Spec: workflow.Spec{Nodes: map[workflow.Identifier]workflow.Node{"assess": workflow.ReasoningNode{Common: workflow.NodeFields{Prompt: &version.Reference}}}}}
	pins, err := resolvePromptSnapshots(t.Context(), contentPlanner{version: version}, document)
	if err != nil || pins["assess"].Reference != version.Reference {
		t.Fatalf("exact published prompt not captured: %v", err)
	}
	if _, err := resolvePromptSnapshots(t.Context(), checkpointPlanner{}, document); err == nil {
		t.Fatal("missing resolver silently omitted linked prompt")
	}
	if _, err := resolvePromptSnapshots(t.Context(), contentPlanner{err: errors.New("unavailable")}, document); err == nil {
		t.Fatal("missing published content silently omitted linked prompt")
	}
	version.Document = contentstore.Document{Kind: "template", Content: "Wrong kind"}
	if _, err := resolvePromptSnapshots(t.Context(), contentPlanner{version: version}, document); err == nil {
		t.Fatal("template substituted for prompt")
	}
}

func TestTemplateLinksResolveBeforeRunInputSnapshot(t *testing.T) {
	version := contentlibrary.BuiltinItems()[1].Versions[0]
	document := workflow.Document{Spec: workflow.Spec{Inputs: map[workflow.Identifier]workflow.ValueDeclaration{"template": {Type: workflow.ValueTemplate, Resource: &workflow.Resource{Source: workflow.TemplateReferenceResource{Reference: version.Reference}}}}}}
	planner := contentPlanner{checkpointPlanner: checkpointPlanner{definition: workflow.Definition{Document: document}}, version: version}
	context, err := derivedRouteContext(t.Context(), planner, CreateRequest{}, statestore.WorkItemProjection{}, statestore.ProjectProjection{})
	if err != nil || len(context.RunInputs["template"]) == 0 {
		t.Fatalf("linked template was not resolved: %v", err)
	}
	planner.err = errors.New("template unavailable")
	if _, err := derivedRouteContext(t.Context(), planner, CreateRequest{}, statestore.WorkItemProjection{}, statestore.ProjectProjection{}); err == nil {
		t.Fatal("linked missing template must fail preparation")
	}
}
