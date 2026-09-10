package nodes

import (
	"darkstar/src/core/workflow"
	"encoding/json"
	"testing"
)

func TestGateEvaluatesEvidenceForBothOutcomes(t *testing.T) {
	n := Gate{Node: workflow.GateNode{Executor: workflow.GateExecutor{Policy: "complete", Condition: workflow.ComparisonPredicate{Operator: workflow.CompareEqual, Args: [2]workflow.Operand{workflow.ReferenceOperand{Ref: "input.progress.remaining"}, workflow.LiteralOperand{Literal: json.RawMessage(`0`)}}}}}}
	for _, remaining := range []string{"0", "1"} {
		raw, err := n.Execute(t.Context(), Inputs{"progress": json.RawMessage(`{"remaining":` + remaining + `}`)}, BuiltinServices{})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Passed   bool `json:"passed"`
			Evidence struct {
				Policy string `json:"policy"`
				Passed bool   `json:"passed"`
			} `json:"gate_evidence"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.Passed != (remaining == "0") || result.Evidence.Passed != result.Passed || result.Evidence.Policy != "complete" {
			t.Fatalf("evidence = %s", raw)
		}
	}
}
