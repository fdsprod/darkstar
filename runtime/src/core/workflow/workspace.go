package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type WorkspacePrepareNode struct {
	Common   NodeFields
	Executor WorkspacePrepareExecutor
}
type WorkspacePrepareExecutor struct {
	RepositoryInput Identifier   `json:"repositoryInput"`
	Checkout        CheckoutPlan `json:"checkout"`
}
type CheckoutPlan interface{ isCheckoutPlan() }
type CurrentCheckout struct{}
type NewWorktree struct {
	BaseRef string `json:"baseRef"`
	Branch  string `json:"branch"`
}

func (CurrentCheckout) isCheckoutPlan() {}
func (NewWorktree) isCheckoutPlan()     {}
func (CurrentCheckout) MarshalJSON() ([]byte, error) {
	return []byte(`{"mode":"current_checkout"}`), nil
}
func (v NewWorktree) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Mode    string `json:"mode"`
		BaseRef string `json:"baseRef"`
		Branch  string `json:"branch"`
	}{"new_worktree", v.BaseRef, v.Branch})
}
func (v *WorkspacePrepareExecutor) UnmarshalJSON(raw []byte) error {
	var wire struct {
		RepositoryInput Identifier      `json:"repositoryInput"`
		Checkout        json.RawMessage `json:"checkout"`
	}
	if err := strictDecode(raw, &wire); err != nil {
		return err
	}
	var mode struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(wire.Checkout, &mode); err != nil {
		return err
	}
	v.RepositoryInput = wire.RepositoryInput
	switch mode.Mode {
	case "current_checkout":
		var c struct {
			Mode string `json:"mode"`
		}
		if err := strictDecode(wire.Checkout, &c); err != nil {
			return err
		}
		v.Checkout = CurrentCheckout{}
	case "new_worktree":
		var c struct {
			Mode    string `json:"mode"`
			BaseRef string `json:"baseRef"`
			Branch  string `json:"branch"`
		}
		if err := strictDecode(wire.Checkout, &c); err != nil {
			return err
		}
		if strings.TrimSpace(c.BaseRef) == "" || strings.TrimSpace(c.Branch) == "" {
			return errors.New("new_worktree requires baseRef and branch")
		}
		v.Checkout = NewWorktree{c.BaseRef, c.Branch}
	default:
		return errors.New("checkout.mode must be current_checkout or new_worktree")
	}
	return nil
}
func (WorkspacePrepareNode) Type() NodeType       { return NodeWorkspacePrepare }
func (n WorkspacePrepareNode) Fields() NodeFields { return n.Common }
func (WorkspacePrepareNode) isNode()              {}
func (n WorkspacePrepareNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(n.Common, n.Type(), "workspacePrepare", n.Executor))
}

type WorkspaceValidateNode struct {
	Common   NodeFields
	Executor WorkspaceValidateExecutor
}
type WorkspaceValidateExecutor struct {
	WorkspaceInput Identifier `json:"workspaceInput"`
	Checks         [][]string `json:"checks"`
}

func (WorkspaceValidateNode) Type() NodeType       { return NodeWorkspaceValidate }
func (n WorkspaceValidateNode) Fields() NodeFields { return n.Common }
func (WorkspaceValidateNode) isNode()              {}
func (n WorkspaceValidateNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(n.Common, n.Type(), "workspaceValidate", n.Executor))
}

// Required connections are enforced by the same validator used for drafts,
// publishing, authoring tools, and installed definitions.
func (s *validationState) validateWorkspace(id Identifier, node Node, fields NodeFields) {
	location := fmt.Sprintf("/spec/nodes/%s", id)
	input := func(name Identifier, kind ValueType) {
		binding, ok := fields.Inputs[name]
		required, yes := binding.(RequiredBinding)
		if !ok || !yes || required.Type != kind {
			s.add(ValidationBindingIncompatible, fmt.Sprintf("Connect required %s input %q", kind, name), location+"/inputs/"+string(name), nil)
		}
	}
	output := func(name Identifier, kind ValueType) {
		v, ok := fields.Outputs[name]
		if !ok || v.Type != kind || (v.Required != nil && !*v.Required) {
			s.add(ValidationSchemaInvalid, fmt.Sprintf("Declare required %s output %q", kind, name), location+"/outputs/"+string(name), nil)
		}
	}
	var raw map[string]json.RawMessage
	encoded, _ := json.Marshal(node)
	_ = json.Unmarshal(encoded, &raw)
	var executor map[string]any
	for _, key := range []string{"workspacePrepare", "workspaceValidate", "implementation"} {
		if raw[key] != nil {
			_ = json.Unmarshal(raw[key], &executor)
		}
	}
	for _, connection := range ComponentRequirements()[node.Type()].Connections {
		id := connection.ID
		if connection.Field != "" {
			value, _ := executor[connection.Field].(string)
			id = Identifier(value)
			if id == "" && connection.LegacyOptional {
				continue
			}
		}
		if connection.Direction == "input" {
			input(id, connection.Type)
		} else {
			output(id, connection.Type)
		}
	}
	switch n := node.(type) {
	case WorkspacePrepareNode:

		switch n.Executor.Checkout.(type) {
		case CurrentCheckout, NewWorktree:
		default:
			s.add(ValidationSchemaInvalid, "Choose current checkout or new worktree", location+"/workspacePrepare/checkout", nil)
		}

	case WorkspaceValidateNode:

		if len(n.Executor.Checks) == 0 {
			s.add(ValidationSchemaInvalid, "Add at least one validation command", location+"/workspaceValidate/checks", nil)
		}
		for _, argv := range n.Executor.Checks {
			if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
				s.add(ValidationSchemaInvalid, "Each check requires an executable and optional arguments", location+"/workspaceValidate/checks", nil)
			}
		}
	}
}
