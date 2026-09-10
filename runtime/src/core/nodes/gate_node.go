package nodes

import (
	"context"
	"darkstar/src/core/workflow"
	"encoding/json"
)

type Gate struct{ Node workflow.GateNode }

func (Gate) isHandler() {}
func (n Gate) Execute(ctx context.Context, inputs Inputs, services BuiltinServices) (json.RawMessage, error) {
	runInputs := Inputs{}
	for k, v := range services.RunInputs {
		runInputs[workflow.Identifier(k)] = v
	}
	return GateOutput(n.Node, inputs, runInputs)
}
func GateOutput(node workflow.GateNode, inputs, runInputs Inputs) (json.RawMessage, error) {
	passed, err := workflow.EvaluatePredicate(node.Executor.Condition, workflow.PredicateValues{Inputs: inputs, RunInputs: runInputs}, "/gate/condition")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"passed": passed, "gate_evidence": map[string]any{"policy": node.Executor.Policy, "passed": passed}})
}
