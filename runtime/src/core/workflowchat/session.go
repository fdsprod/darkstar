// Package workflowchat implements the draft-only capability boundary for authoring agents.
package workflowchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/workflowstore"
)

// Store deliberately has no publish, install, archive, or execution capability.
type Store interface {
	AuthoringCatalog(context.Context) (workflow.AuthoringCatalog, error)
	Definition(context.Context, string, string) (workflow.Definition, error)
	CreateDraft(context.Context, workflow.DraftCreateRequest) (workflowstore.Draft, error)
	DuplicateDraft(context.Context, string, string, string, workflowstore.DraftScope, string, string) (workflowstore.Draft, error)
	Draft(context.Context, string) (workflowstore.Draft, error)
	UpdateDraft(context.Context, workflow.DraftUpdateRequest) (workflowstore.Draft, error)
	ValidateDraft(context.Context, string, uint64) (workflow.DraftValidationReport, error)
}

type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Target is a wire union, validated before any provider work or mutation.
type Target struct {
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
}

func (t Target) Validate() error {
	switch t.Kind {
	case "new":
		if t.ID == "" && t.Revision == 0 && t.Name == "" && t.Version == "" {
			return nil
		}
	case "draft":
		if t.ID != "" && t.Revision > 0 && t.Name == "" && t.Version == "" {
			return nil
		}
	case "version":
		if t.ID == "" && t.Revision == 0 && t.Name != "" && t.Version != "" {
			return nil
		}
	}
	return errors.New("target must be new, an exact draft revision, or an exact installed version")
}

type Request struct {
	Target     Target      `json:"target"`
	Messages   []Message   `json:"messages"`
	Generation *Generation `json:"generation,omitempty"`
}

// Omission inherits provider configuration; explicit choices are checked as a
// model/effort pair against the provider's current catalog before generation.
type Generation struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"defaultEffort"`
	IsDefault     bool     `json:"isDefault"`
}

type ModelLister interface {
	Models(context.Context) ([]Model, error)
}

func (r Request) Validate() error {
	if r.Generation != nil && (strings.TrimSpace(r.Generation.Model) == "" || strings.TrimSpace(r.Generation.Effort) == "" || len(r.Generation.Model) > 200 || len(r.Generation.Effort) > 40) {
		return errors.New("generation requires a model and supported effort")
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if len(r.Messages) == 0 || len(r.Messages) > 100 || r.Messages[len(r.Messages)-1].Role != "user" {
		return errors.New("provide 1–100 messages ending with a user message")
	}
	for _, m := range r.Messages {
		if (m.Role != "user" && m.Role != "assistant") || strings.TrimSpace(m.Text) == "" || len(m.Text) > 32000 {
			return errors.New("invalid chat message")
		}
	}
	return nil
}

// Events are tagged on the wire. Payloads belong to their individual event kind.
type Emit func(kind string, payload any) error
type Runner interface {
	Run(context.Context, *Session, []Message, Emit) error
}

type Session struct {
	Store      Store
	Target     Target
	Key        string
	Emit       Emit
	Generation *Generation
	blocked    bool
	created    bool
}

func (s *Session) Definitions() []provider.ToolDefinition {
	text := map[string]any{"type": "string"}
	object := map[string]any{"type": "object", "additionalProperties": true}
	tool := func(name, description string, properties map[string]any, required ...string) provider.ToolDefinition {
		if required == nil {
			required = []string{}
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required})
		return provider.ToolDefinition{Type: "function", Name: name, Description: description, InputSchema: schema}
	}
	return []provider.ToolDefinition{
		tool("inspect_workflow", "Read the selected workflow and the available authoring references. Always inspect before editing.", map[string]any{}),
		tool("create_workflow", "Create a new draft when the user requests a new workflow, even while another workflow is selected. Choose a sensible name if none was provided. The new draft becomes the editing target; the previous selection is preserved. Use edit_workflow for changes to the current workflow. Never publishes.", map[string]any{"name": text, "document": object}, "name", "document"),
		tool("edit_workflow", "Save a complete replacement document for the selected workflow. Automatically forks an immutable version on first edit. Preserve unrelated fields. Every save updates the user's canvas. Never publishes.", map[string]any{"document": object}, "document"),
		tool("validate_workflow", "Validate the saved draft and inspect authoritative findings. Fix findings or discuss missing references with the user.", map[string]any{}),
		tool("ask_question", "Ask a question or explain a conflict with the user's requirements and suggest concrete resolutions. Ends editing for this turn; await the user's next message.", map[string]any{"question": text, "options": map[string]any{"type": "array", "items": text, "maxItems": 5}}, "question", "options"),
	}
}

func (s *Session) Call(ctx context.Context, _ string, name string, raw json.RawMessage) (json.RawMessage, error) {
	if s.blocked && name != "inspect_workflow" && name != "ask_question" {
		return nil, errors.New("editing paused; await the user's response in a new turn")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var value any
	var err error
	switch name {
	case "inspect_workflow":
		if err = decode(raw, &struct{}{}); err != nil {
			break
		}
		var selected any
		switch s.Target.Kind {
		case "draft":
			selected, err = s.Store.Draft(ctx, s.Target.ID)
		case "version":
			selected, err = s.Store.Definition(ctx, s.Target.Name, s.Target.Version)
		}
		if err != nil {
			break
		}
		var catalog workflow.AuthoringCatalog
		catalog, err = s.Store.AuthoringCatalog(ctx)
		value = map[string]any{"target": s.Target, "workflow": selected, "catalog": catalog, "componentRequirements": workflow.ComponentRequirements(), "workspaceExample": json.RawMessage(workspaceExample), "starterDocument": json.RawMessage(starterDocument), "singleStepExample": json.RawMessage(workspaceExample), "reasoningExample": json.RawMessage(reasoningStepDocument), "executionContext": map[string]string{"reasoningAgent": "The reasoning.agent field is a descriptive role label passed to the configured runtime provider, not a reference to an agent registry. Use general-purpose for ordinary agent work, or preserve an appropriate existing role. An unavailable catalog.agents list does not block creating a reasoning node.", "taskInput": "A spec input with type task and resource kind task receives the work item from the runtime; bind the node task input from run.input.task. Do not ask the user to manually supply the work item.", "access": "Reasoning nodes execute read-only. Use implementation to modify repository files and run checks; it requires process.run and workspace.write permissions and a changeset object output. The runtime verifies actual workspace changes. Supporting Markdown inputs are optional; no plan is required. Publishing remains human only."}}
	case "create_workflow", "edit_workflow":
		var args struct {
			Name     string          `json:"name,omitempty"`
			Document json.RawMessage `json:"document"`
		}
		if err = decode(raw, &args); err != nil {
			break
		}
		if len(args.Document) == 0 || args.Document[0] != '{' {
			err = errors.New("document must be a JSON object")
			break
		}
		if name == "create_workflow" && s.created {
			err = errors.New("a draft was already created in this turn; edit it instead")
			break
		}
		if name == "edit_workflow" && (s.Target.Kind == "new" || args.Name != "") {
			err = errors.New("select or create a workflow before editing; name belongs in document metadata")
			break
		}
		var draft workflowstore.Draft
		if name == "create_workflow" {
			draft, err = s.Store.CreateDraft(ctx, workflow.DraftCreateRequest{Name: args.Name, Scope: workflowstore.DraftScopeUser, ScopeReference: "local-user", IdempotencyKey: s.Key, Document: args.Document})
			if err == nil {
				s.created = true
			}
		} else {
			if s.Target.Kind == "version" {
				draft, err = s.Store.DuplicateDraft(ctx, s.Target.Name, s.Target.Version, s.Target.Name, workflowstore.DraftScopeUser, "local-user", s.Key)
				if err != nil {
					break
				}
				if err = s.saved(draft); err != nil {
					break
				}
			}
			draft, err = s.Store.UpdateDraft(ctx, workflow.DraftUpdateRequest{ID: s.Target.ID, ExpectedRevision: s.Target.Revision, Document: args.Document})
		}
		if err == nil {
			err = s.saved(draft)
			value = draft
			if err == nil {
				report, validationErr := s.Store.ValidateDraft(ctx, draft.ID, draft.Revision)
				if validationErr == nil {
					err = s.Emit("validation", report)
					value = map[string]any{"draft": draft, "validation": report}
				}
			}
		}
		if errors.Is(err, workflowstore.ErrDraftConflict) {
			s.blocked = true
			remote, readErr := s.Store.Draft(ctx, s.Target.ID)
			if readErr != nil {
				return nil, readErr
			}
			err = s.Emit("conflict", map[string]any{"remote": remote, "message": "Another editor changed this draft. Review the latest revision, then ask the agent to reapply your request or revise it. No conflicting edit was saved."})
			if err == nil {
				latest, marshalErr := json.Marshal(remote)
				if marshalErr != nil {
					return nil, marshalErr
				}
				err = fmt.Errorf("revision conflict: no edit was saved. Compare the requested change with this latest draft and ask the user how to reconcile them. Latest draft: %s", latest)
			}
		}
	case "validate_workflow":
		if err = decode(raw, &struct{}{}); err != nil {
			break
		}
		if s.Target.Kind != "draft" {
			err = errors.New("create or edit a draft before validation")
			break
		}
		value, err = s.Store.ValidateDraft(ctx, s.Target.ID, s.Target.Revision)
		if err == nil {
			err = s.Emit("validation", value)
		}
	case "ask_question":
		var question struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
		}
		if err = decode(raw, &question); err != nil {
			break
		}
		if strings.TrimSpace(question.Question) == "" || len(question.Options) > 5 {
			err = errors.New("provide a question and at most five options")
			break
		}
		s.blocked = true
		err = s.Emit("question", question)
		value = map[string]string{"status": "awaiting_user"}
	default:
		err = fmt.Errorf("tool %q is not available; publishing is human only", name)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// An editable scaffold, not an executable recommendation. The agent replaces
// the command and metadata with the user's requested behavior before validation.
const starterDocument = `{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"workflow/new-workflow","version":"0.1.0"},"spec":{"routeDefaults":{"entry":"start","terminals":["start"]},"nodes":{"start":{"displayName":"Start","type":"command","entry":true,"terminal":true,"inputs":{},"outputs":{},"command":{"argv":["replace-with-requested-command"]},"transitions":[]}}}}`

func (s *Session) saved(draft workflowstore.Draft) error {
	s.Target = Target{Kind: "draft", ID: draft.ID, Revision: draft.Revision}
	return s.Emit("draft", draft)
}

const reasoningStepDocument = `{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"workflow/single-shot","version":"0.1.0","displayName":"Single Shot"},"spec":{"inputs":{"task":{"type":"task","resource":{"kind":"task"}}},"routeDefaults":{"entry":"fulfill","terminals":["fulfill"]},"nodes":{"fulfill":{"type":"reasoning","displayName":"Fulfill work item","entry":true,"terminal":true,"inputs":{"task":{"type":"task","from":"run.input.task"}},"outputs":{"result":{"type":"string"}},"reasoning":{"agent":"general-purpose","instructions":"Read the connected work item and produce the result it asks for. Return the complete requested result under the result output. Do not introduce planning or review stages."},"transitions":[]}}}}`

const singleStepDocument = `{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"workflow/single-shot","version":"0.1.0","displayName":"Single Shot"},"spec":{"inputs":{"task":{"type":"task","resource":{"kind":"task"}}},"routeDefaults":{"entry":"implement","terminals":["implement"]},"nodes":{"implement":{"type":"implementation","displayName":"Implement work item","entry":true,"terminal":true,"inputs":{"task":{"type":"task","from":"run.input.task"}},"outputs":{"changeset":{"type":"object"}},"implementation":{"taskInput":"task","instructions":"Make the changes requested by the connected work item in the repository and run relevant checks. Do not publish."},"permissions":["process.run","workspace.write"],"transitions":[]}}}}`

func decode(raw []byte, target any) error {
	if len(raw) > 2<<20 {
		return errors.New("tool input exceeds 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
