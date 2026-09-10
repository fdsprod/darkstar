package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"darkstar/src/core/workflow"
)

type WorkspaceValidate struct {
	Node workflow.WorkspaceValidateNode
}

func (WorkspaceValidate) isHandler() {}

func (n WorkspaceValidate) Execute(ctx context.Context, inputs Inputs, services BuiltinServices) (json.RawMessage, error) {
	p, err := services.Workspaces.Store.Resolve(ctx, inputs[n.Node.Executor.WorkspaceInput])
	if err != nil {
		return nil, err
	}
	if len(n.Node.Executor.Checks) == 0 {
		return nil, errors.New("at least one required check must be configured")
	}
	results := []map[string]any{}
	for _, argv := range n.Node.Executor.Checks {
		if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
			return nil, errors.New("validation check requires an executable")
		}
		output, err := services.Commands.Run(ctx, p.Path, argv, 2*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("required check %q failed: %w\n%s", argv, err, output)
		}
		results = append(results, map[string]any{"argv": argv, "exitCode": 0, "output": output})
	}
	return json.Marshal(map[string]any{"validation": map[string]any{"workspaceId": p.ID, "passed": true, "checks": results}})
}
