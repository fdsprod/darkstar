package workflow

import (
	"encoding/json"
	"fmt"

	"darkstar/src/ports/extension"
)

const NodeExtension NodeType = "extension"

// ExtensionExecutor pins custom behavior. Workflow authors supply data only;
// executable registration and permission grants belong to the daemon.
type ExtensionExecutor struct {
	Ref           extension.Ref   `json:"ref"`
	Configuration json.RawMessage `json:"configuration"`
}

func (e ExtensionExecutor) Validate() error {
	if err := e.Ref.Validate(); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(e.Configuration, &object) != nil || object == nil {
		return fmt.Errorf("extension configuration must be an object")
	}
	return nil
}

type ExtensionNode struct {
	Common   NodeFields
	Executor ExtensionExecutor
}

func (ExtensionNode) isNode()              {}
func (ExtensionNode) Type() NodeType       { return NodeExtension }
func (n ExtensionNode) Fields() NodeFields { return n.Common }
func (n ExtensionNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(n.Common, n.Type(), "extension", n.Executor))
}

// ExtensionValidator uses the same pinned implementation/configuration shape.
type ExtensionValidator struct {
	Extension ExtensionExecutor `json:"extension"`
}

func (ExtensionValidator) isValidator() {}
