package nodes

import (
	"context"
	"encoding/json"
	"fmt"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/nodeextension"
)

type Extension struct{ Node workflow.ExtensionNode }

func (Extension) isHandler() {}

func (n Extension) Execute(ctx context.Context, inputs Inputs, services BuiltinServices) (json.RawMessage, error) {
	if err := n.Node.Executor.Validate(); err != nil {
		return nil, err
	}
	if services.Extensions == nil {
		return nil, fmt.Errorf("EXTENSION_UNAVAILABLE: %s", n.Node.Executor.Ref.ID)
	}
	executor, err := services.Extensions.Configure(n.Node.Executor.Ref, n.Node.Executor.Configuration)
	if err != nil {
		return nil, err
	}
	scoped := map[string]json.RawMessage{}
	for name := range n.Node.Common.Inputs {
		if value, ok := inputs[name]; ok {
			scoped[string(name)] = append(json.RawMessage(nil), value...)
		}
	}
	outputs, err := executor.Execute(ctx, nodeextension.Request{Configuration: append(json.RawMessage(nil), n.Node.Executor.Configuration...), Inputs: scoped})
	if err != nil {
		return nil, err
	}
	return json.Marshal(outputs)
}
