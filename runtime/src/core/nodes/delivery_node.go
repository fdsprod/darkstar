package nodes

import (
	"context"
	"darkstar/src/core/workflow"
	"encoding/json"
	"errors"
)

type Delivery struct{ Node workflow.Node }

func (Delivery) isHandler() {}
func (n Delivery) Execute(ctx context.Context, inputs Inputs, services BuiltinServices) (json.RawMessage, error) {
	if services.Delivery == nil {
		return nil, errors.New("delivery service unavailable")
	}
	return services.Delivery.Execute(ctx, n.Node, inputs)
}
