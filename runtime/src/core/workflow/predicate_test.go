package workflow_test

import (
	"encoding/json"
	"errors"
	"testing"

	"darkstar/src/core/workflow"
)

const predicateLocation = "/spec/nodes/gate/gate/condition"

func TestEvaluatePredicateUsesStrictTypedComparisons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		predicate workflow.Predicate
		want      bool
	}{
		{name: "constant", predicate: workflow.ConstantPredicate{Value: true}, want: true},
		{name: "all false", predicate: workflow.LogicalPredicate{Operator: workflow.LogicalAll, Args: []workflow.Predicate{workflow.ConstantPredicate{Value: true}, workflow.ConstantPredicate{Value: false}}}, want: false},
		{name: "any true", predicate: workflow.LogicalPredicate{Operator: workflow.LogicalAny, Args: []workflow.Predicate{workflow.ConstantPredicate{Value: false}, workflow.ConstantPredicate{Value: true}}}, want: true},
		{name: "equal integers", predicate: comparison(workflow.CompareEqual, literal("1"), literal("1")), want: true},
		{name: "integer and number differ", predicate: comparison(workflow.CompareEqual, literal("1"), literal("1.0")), want: false},
		{name: "equivalent numbers", predicate: comparison(workflow.CompareEqual, literal("1.00"), literal("1e0")), want: true},
		{name: "deep object key order", predicate: comparison(workflow.CompareEqual, literal(`{"b":[true,null],"a":1}`), literal(`{"a":1,"b":[true,null]}`)), want: true},
		{name: "different nested numeric types", predicate: comparison(workflow.CompareNotEqual, literal(`{"score":1}`), literal(`{"score":1.0}`)), want: true},
		{name: "exact large-number order", predicate: comparison(workflow.CompareGreater, literal("9007199254740993"), literal("9007199254740992")), want: true},
		{name: "unicode string order", predicate: comparison(workflow.CompareLess, literal(`"z"`), literal(`"é"`)), want: true},
		{name: "less or equal", predicate: comparison(workflow.CompareLessOrEqual, literal("2.5"), literal("2.50")), want: true},
		{name: "greater or equal", predicate: comparison(workflow.CompareGreaterOrEqual, literal("3"), literal("2.9")), want: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := workflow.EvaluatePredicate(test.predicate, workflow.PredicateValues{}, predicateLocation)
			if err != nil || got != test.want {
				t.Fatalf("EvaluatePredicate() = %v, %v; want %v, nil", got, err, test.want)
			}
		})
	}
}

func TestEvaluatePredicateResolvesRootsMembersAndPresence(t *testing.T) {
	t.Parallel()

	values := workflow.PredicateValues{
		Outputs: map[workflow.Identifier]json.RawMessage{
			"assessment": json.RawMessage(`{"score":0.75,"evidence":{"approved":true}}`),
		},
		Inputs: map[workflow.Identifier]json.RawMessage{
			"optional": json.RawMessage("null"),
		},
		RunInputs: map[workflow.Identifier]json.RawMessage{
			"priority": json.RawMessage(`"urgent"`),
		},
	}
	predicate := workflow.LogicalPredicate{Operator: workflow.LogicalAll, Args: []workflow.Predicate{
		comparison(workflow.CompareGreaterOrEqual, reference("output.assessment.score"), literal("0.5")),
		comparison(workflow.CompareEqual, reference("output.assessment.evidence.approved"), literal("true")),
		comparison(workflow.CompareEqual, reference("run.input.priority"), literal(`"urgent"`)),
		workflow.PresentPredicate{Reference: reference("input.optional")},
		workflow.NotPredicate{Arg: workflow.PresentPredicate{Reference: reference("input.absent")}},
	}}

	got, err := workflow.EvaluatePredicate(predicate, values, predicateLocation)
	if err != nil || !got {
		t.Fatalf("EvaluatePredicate() = %v, %v; want true, nil", got, err)
	}
}

func TestEvaluatePredicateReturnsFirstAuthoredErrorWithoutShortCircuiting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		operator workflow.LogicalOperator
		first    bool
	}{
		{name: "all evaluates after false", operator: workflow.LogicalAll, first: false},
		{name: "any evaluates after true", operator: workflow.LogicalAny, first: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			predicate := workflow.LogicalPredicate{Operator: test.operator, Args: []workflow.Predicate{
				workflow.ConstantPredicate{Value: test.first},
				comparison(workflow.CompareEqual, reference("output.first_missing"), literal("1")),
				comparison(workflow.CompareEqual, reference("output.second_missing"), literal("1")),
			}}
			_, err := workflow.EvaluatePredicate(predicate, workflow.PredicateValues{}, predicateLocation)
			assertPredicateError(t, err, "predicate reference 'output.first_missing' is missing", predicateLocation+"/args/1/args/0/ref")
		})
	}
}

func TestEvaluatePredicateReportsStableRuntimeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		predicate workflow.Predicate
		values    workflow.PredicateValues
		message   string
		location  string
	}{
		{
			name: "missing reference", predicate: comparison(workflow.CompareEqual, reference("output.score"), literal("1")),
			message: "predicate reference 'output.score' is missing", location: predicateLocation + "/args/0/ref",
		},
		{
			name: "invalid reference", predicate: comparison(workflow.CompareEqual, reference("node.assess.output.score"), literal("1")),
			message: "predicate reference 'node.assess.output.score' is invalid", location: predicateLocation + "/args/0/ref",
		},
		{
			name: "incomparable values", predicate: comparison(workflow.CompareLess, literal("true"), literal("1")),
			message: "operator 'lt' requires two numbers or two strings", location: predicateLocation,
		},
		{
			name: "invalid snapshot JSON", predicate: comparison(workflow.CompareEqual, reference("output.score"), literal("1")),
			values:  workflow.PredicateValues{Outputs: map[workflow.Identifier]json.RawMessage{"score": json.RawMessage("not-json")}},
			message: "predicate value is invalid JSON", location: predicateLocation + "/args/0/ref",
		},
		{
			name: "missing predicate", predicate: nil,
			message: "predicate is missing", location: predicateLocation,
		},
		{
			name: "unknown comparison operator", predicate: comparison(workflow.ComparisonOperator("matches"), reference("output.missing"), literal("1")),
			message: "unknown predicate operator 'matches'", location: predicateLocation,
		},
		{
			name: "unknown logical operator", predicate: workflow.LogicalPredicate{Operator: workflow.LogicalOperator("xor"), Args: []workflow.Predicate{workflow.ConstantPredicate{Value: true}}},
			message: "unknown predicate operator 'xor'", location: predicateLocation,
		},
		{
			name: "empty logical operands", predicate: workflow.LogicalPredicate{Operator: workflow.LogicalAll},
			message: "operator 'all' requires predicates", location: predicateLocation,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := workflow.EvaluatePredicate(test.predicate, test.values, predicateLocation)
			assertPredicateError(t, err, test.message, test.location)
		})
	}
}

func comparison(operator workflow.ComparisonOperator, left, right workflow.Operand) workflow.ComparisonPredicate {
	return workflow.ComparisonPredicate{Operator: operator, Args: [2]workflow.Operand{left, right}}
}

func literal(value string) workflow.LiteralOperand {
	return workflow.LiteralOperand{Literal: json.RawMessage(value)}
}

func reference(value string) workflow.ReferenceOperand {
	return workflow.ReferenceOperand{Ref: value}
}

func assertPredicateError(t *testing.T, err error, message, location string) {
	t.Helper()
	var failure *workflow.FrameError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T %v; want *workflow.FrameError", err, err)
	}
	if failure.Code != workflow.RunPredicateInvalid || failure.Message != message || failure.Location != location {
		t.Fatalf("error = %+v; want code %q, message %q, location %q", failure, workflow.RunPredicateInvalid, message, location)
	}
}
