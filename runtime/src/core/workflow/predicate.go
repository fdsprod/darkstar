package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

const RunPredicateInvalid = "RUN_PREDICATE_INVALID"

// PredicateValues is the immutable value snapshot visible to one predicate
// evaluation. Map membership represents presence, so a present JSON null never
// collapses into the missing state.
type PredicateValues struct {
	Outputs   map[Identifier]json.RawMessage
	Inputs    map[Identifier]json.RawMessage
	RunInputs map[Identifier]json.RawMessage
}

// EvaluatePredicate evaluates the closed v1alpha1 predicate language against
// one value snapshot. Location is the JSON Pointer of predicate in the frozen
// workflow and is extended to identify the first failing authored operand.
func EvaluatePredicate(predicate Predicate, values PredicateValues, location string) (bool, error) {
	evaluator := predicateEvaluator{values: values}
	return evaluator.evaluate(predicate, location)
}

type predicateEvaluator struct {
	values PredicateValues
}

func (e predicateEvaluator) evaluate(predicate Predicate, location string) (bool, error) {
	switch value := predicate.(type) {
	case ConstantPredicate:
		return value.Value, nil
	case ComparisonPredicate:
		return e.evaluateComparison(value, location)
	case PresentPredicate:
		_, present, err := e.resolve(value.Reference.Ref, pointerChild(location, "arg/ref"))
		return present, err
	case LogicalPredicate:
		return e.evaluateLogical(value, location)
	case NotPredicate:
		result, err := e.evaluate(value.Arg, pointerChild(location, "arg"))
		if err != nil {
			return false, err
		}
		return !result, nil
	case nil:
		return false, predicateFailure("predicate is missing", location)
	default:
		return false, predicateFailure(fmt.Sprintf("unsupported predicate type %T", predicate), location)
	}
}

func (e predicateEvaluator) evaluateComparison(predicate ComparisonPredicate, location string) (bool, error) {
	if !validComparisonOperator(predicate.Operator) {
		return false, predicateFailure(fmt.Sprintf("unknown predicate operator '%s'", predicate.Operator), location)
	}
	left, err := e.operand(predicate.Args[0], pointerChild(location, "args/0"))
	if err != nil {
		return false, err
	}
	right, err := e.operand(predicate.Args[1], pointerChild(location, "args/1"))
	if err != nil {
		return false, err
	}

	switch predicate.Operator {
	case CompareEqual, CompareNotEqual:
		equal, err := equalPredicateValues(left, right)
		if err != nil {
			return false, predicateFailure("comparison contains an invalid JSON number", location)
		}
		if predicate.Operator == CompareNotEqual {
			return !equal, nil
		}
		return equal, nil
	case CompareLess, CompareLessOrEqual, CompareGreater, CompareGreaterOrEqual:
		order, comparable, err := comparePredicateValues(left, right)
		if err != nil {
			return false, predicateFailure("comparison contains an invalid JSON number", location)
		}
		if !comparable {
			return false, predicateFailure(
				fmt.Sprintf("operator '%s' requires two numbers or two strings", predicate.Operator), location)
		}
		switch predicate.Operator {
		case CompareLess:
			return order < 0, nil
		case CompareLessOrEqual:
			return order <= 0, nil
		case CompareGreater:
			return order > 0, nil
		default:
			return order >= 0, nil
		}
	default:
		return false, predicateFailure(fmt.Sprintf("unknown predicate operator '%s'", predicate.Operator), location)
	}
}

func (e predicateEvaluator) evaluateLogical(predicate LogicalPredicate, location string) (bool, error) {
	if predicate.Operator != LogicalAll && predicate.Operator != LogicalAny {
		return false, predicateFailure(fmt.Sprintf("unknown predicate operator '%s'", predicate.Operator), location)
	}
	if len(predicate.Args) == 0 {
		return false, predicateFailure(fmt.Sprintf("operator '%s' requires predicates", predicate.Operator), location)
	}

	result := predicate.Operator == LogicalAll
	for index, arg := range predicate.Args {
		value, err := e.evaluate(arg, pointerChild(location, fmt.Sprintf("args/%d", index)))
		if err != nil {
			return false, err
		}
		switch predicate.Operator {
		case LogicalAll:
			result = result && value
		case LogicalAny:
			result = result || value
		}
	}
	return result, nil
}

func validComparisonOperator(operator ComparisonOperator) bool {
	switch operator {
	case CompareEqual, CompareNotEqual, CompareLess, CompareLessOrEqual, CompareGreater, CompareGreaterOrEqual:
		return true
	default:
		return false
	}
}

func (e predicateEvaluator) operand(operand Operand, location string) (any, error) {
	switch value := operand.(type) {
	case LiteralOperand:
		return decodePredicateValue(value.Literal, location)
	case ReferenceOperand:
		resolved, present, err := e.resolve(value.Ref, pointerChild(location, "ref"))
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, predicateFailure(fmt.Sprintf("predicate reference '%s' is missing", value.Ref), pointerChild(location, "ref"))
		}
		return resolved, nil
	case nil:
		return nil, predicateFailure("operand is missing", location)
	default:
		return nil, predicateFailure(fmt.Sprintf("unsupported operand type %T", operand), location)
	}
}

func (e predicateEvaluator) resolve(reference, location string) (any, bool, error) {
	parts := strings.Split(reference, ".")
	var raw json.RawMessage
	var present bool
	var tail []string

	switch {
	case len(parts) >= 2 && parts[0] == "output":
		raw, present = e.values.Outputs[Identifier(parts[1])]
		tail = parts[2:]
	case len(parts) >= 2 && parts[0] == "input":
		raw, present = e.values.Inputs[Identifier(parts[1])]
		tail = parts[2:]
	case len(parts) >= 3 && parts[0] == "run" && parts[1] == "input":
		raw, present = e.values.RunInputs[Identifier(parts[2])]
		tail = parts[3:]
	default:
		return nil, false, predicateFailure(fmt.Sprintf("predicate reference '%s' is invalid", reference), location)
	}
	if !present {
		return nil, false, nil
	}

	current, err := decodePredicateValue(raw, location)
	if err != nil {
		return nil, false, err
	}
	for _, member := range tail {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		current, present = object[member]
		if !present {
			return nil, false, nil
		}
	}
	return current, true, nil
}

func decodePredicateValue(raw json.RawMessage, location string) (any, error) {
	if !json.Valid(raw) {
		return nil, predicateFailure("predicate value is invalid JSON", location)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, predicateFailure("predicate value is invalid JSON", location)
	}
	return value, nil
}

func equalPredicateValues(left, right any) (bool, error) {
	leftType := predicateValueType(left)
	if leftType != predicateValueType(right) {
		return false, nil
	}

	switch leftValue := left.(type) {
	case nil:
		return true, nil
	case bool:
		return leftValue == right.(bool), nil
	case string:
		return leftValue == right.(string), nil
	case json.Number:
		return equalPredicateNumbers(leftValue, right.(json.Number))
	case []any:
		rightValue := right.([]any)
		if len(leftValue) != len(rightValue) {
			return false, nil
		}
		for index := range leftValue {
			equal, err := equalPredicateValues(leftValue[index], rightValue[index])
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	case map[string]any:
		rightValue := right.(map[string]any)
		if len(leftValue) != len(rightValue) {
			return false, nil
		}
		for key, item := range leftValue {
			other, exists := rightValue[key]
			if !exists {
				return false, nil
			}
			equal, err := equalPredicateValues(item, other)
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported predicate value %T", left)
	}
}

func equalPredicateNumbers(left, right json.Number) (bool, error) {
	if predicateValueType(left) != predicateValueType(right) {
		return false, nil
	}
	leftValue, ok := new(big.Rat).SetString(left.String())
	if !ok {
		return false, fmt.Errorf("invalid JSON number %q", left)
	}
	rightValue, ok := new(big.Rat).SetString(right.String())
	if !ok {
		return false, fmt.Errorf("invalid JSON number %q", right)
	}
	return leftValue.Cmp(rightValue) == 0, nil
}

func comparePredicateValues(left, right any) (int, bool, error) {
	if leftString, ok := left.(string); ok {
		rightString, comparable := right.(string)
		if !comparable {
			return 0, false, nil
		}
		return strings.Compare(leftString, rightString), true, nil
	}

	leftNumber, ok := left.(json.Number)
	if !ok {
		return 0, false, nil
	}
	rightNumber, comparable := right.(json.Number)
	if !comparable {
		return 0, false, nil
	}
	leftValue, ok := new(big.Rat).SetString(leftNumber.String())
	if !ok {
		return 0, false, fmt.Errorf("invalid JSON number %q", leftNumber)
	}
	rightValue, ok := new(big.Rat).SetString(rightNumber.String())
	if !ok {
		return 0, false, fmt.Errorf("invalid JSON number %q", rightNumber)
	}
	return leftValue.Cmp(rightValue), true, nil
}

func predicateValueType(value any) ValueType {
	switch value := value.(type) {
	case nil:
		return ValueNull
	case bool:
		return ValueBoolean
	case string:
		return ValueString
	case json.Number:
		if strings.ContainsAny(value.String(), ".eE") {
			return ValueNumber
		}
		return ValueInteger
	case []any:
		return ValueArray
	case map[string]any:
		return ValueObject
	default:
		return ""
	}
}

func predicateFailure(message, location string) error {
	return &FrameError{Code: RunPredicateInvalid, Message: message, Location: location}
}

func pointerChild(location, child string) string {
	if location == "" {
		return "/" + child
	}
	return strings.TrimSuffix(location, "/") + "/" + child
}
