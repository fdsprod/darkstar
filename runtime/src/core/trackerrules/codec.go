package trackerrules

import (
	"encoding/json"
	"fmt"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports/tracker"
)

func Decode(encoded []byte) (RuleSet, error) {
	var rules RuleSet
	if err := trackercontract.DecodeClosedJSON(encoded, &rules); err != nil {
		return RuleSet{}, err
	}
	if err := ValidateShape(rules); err != nil {
		return RuleSet{}, err
	}
	return rules, nil
}

func Encode(rules RuleSet) ([]byte, error) {
	if err := ValidateShape(rules); err != nil {
		return nil, err
	}
	return json.Marshal(rules)
}

func (Noop) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"noop"}`), nil
}

func (action Admit) MarshalJSON() ([]byte, error) {
	type payload Admit
	return json.Marshal(struct {
		Kind string `json:"kind"`
		payload
	}{Kind: "admit", payload: payload(action)})
}

func (action Report) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}{Kind: "report", Body: action.Body})
}

type fieldValueWire struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

func (action Transition) MarshalJSON() ([]byte, error) {
	fields := make(map[string]fieldValueWire, len(action.Fields))
	for id, value := range action.Fields {
		var kind string
		switch value.(type) {
		case tracker.TextValue:
			kind = "text"
		case tracker.NumberValue:
			kind = "number"
		case tracker.IDsValue:
			kind = "ids"
		default:
			return nil, fmt.Errorf("unsupported transition field value")
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[id] = fieldValueWire{Kind: kind, Value: encoded}
	}
	return json.Marshal(struct {
		Kind         string                    `json:"kind"`
		TransitionID string                    `json:"transitionId"`
		Fields       map[string]fieldValueWire `json:"fields"`
	}{Kind: "transition", TransitionID: action.TransitionID, Fields: fields})
}

func actionKind(encoded []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := trackercontract.DecodeClosedJSON(encoded, &fields); err != nil {
		return "", err
	}
	var kind string
	if err := json.Unmarshal(fields["kind"], &kind); err != nil {
		return "", fmt.Errorf("action kind is required")
	}
	return kind, nil
}

func decodeIntake(encoded []byte) (IntakeAction, error) {
	kind, err := actionKind(encoded)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "noop":
		var wire struct {
			Kind string `json:"kind"`
		}
		if err := trackercontract.DecodeClosedJSON(encoded, &wire); err != nil {
			return nil, err
		}
		return Noop{}, nil
	case "admit":
		type payload Admit
		var wire struct {
			Kind string `json:"kind"`
			payload
		}
		if err := trackercontract.DecodeClosedJSON(encoded, &wire); err != nil {
			return nil, err
		}
		return Admit(wire.payload), nil
	default:
		return nil, fmt.Errorf("unsupported intake action %q", kind)
	}
}

func decodeOutbound(encoded []byte) (OutboundAction, error) {
	kind, err := actionKind(encoded)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "noop":
		_, err := decodeIntake(encoded)
		return Noop{}, err
	case "report":
		var wire struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
		}
		if err := trackercontract.DecodeClosedJSON(encoded, &wire); err != nil {
			return nil, err
		}
		return Report{Body: wire.Body}, nil
	case "transition":
		var wire struct {
			Kind         string                    `json:"kind"`
			TransitionID string                    `json:"transitionId"`
			Fields       map[string]fieldValueWire `json:"fields"`
		}
		if err := trackercontract.DecodeClosedJSON(encoded, &wire); err != nil {
			return nil, err
		}
		action := Transition{TransitionID: wire.TransitionID, Fields: make(map[string]tracker.FieldValue)}
		for id, value := range wire.Fields {
			if string(value.Value) == "null" || len(value.Value) == 0 {
				return nil, fmt.Errorf("transition fields require an explicit typed value")
			}
			switch value.Kind {
			case "text":
				var decoded tracker.TextValue
				err = json.Unmarshal(value.Value, &decoded)
				action.Fields[id] = decoded
			case "number":
				var decoded tracker.NumberValue
				err = json.Unmarshal(value.Value, &decoded)
				action.Fields[id] = decoded
			case "ids":
				var decoded tracker.IDsValue
				err = json.Unmarshal(value.Value, &decoded)
				action.Fields[id] = decoded
			default:
				return nil, fmt.Errorf("unsupported transition field kind")
			}
			if err != nil {
				return nil, err
			}
		}
		return action, nil
	default:
		return nil, fmt.Errorf("unsupported outbound action %q", kind)
	}
}

func (rule *IntakeRule) UnmarshalJSON(encoded []byte) error {
	var wire struct {
		ID     string          `json:"id"`
		When   Conditions      `json:"when"`
		Action json.RawMessage `json:"action"`
	}
	if err := trackercontract.DecodeClosedJSON(encoded, &wire); err != nil {
		return err
	}
	action, err := decodeIntake(wire.Action)
	if err != nil {
		return err
	}
	*rule = IntakeRule{ID: wire.ID, When: wire.When, Action: action}
	return nil
}

func (rule *OutboundRule) UnmarshalJSON(encoded []byte) error {
	var wire struct {
		ID        string            `json:"id"`
		When      Conditions        `json:"when"`
		Milestone MilestoneContract `json:"milestone"`
		Action    json.RawMessage   `json:"action"`
	}
	if err := trackercontract.DecodeClosedJSON(encoded, &wire); err != nil {
		return err
	}
	action, err := decodeOutbound(wire.Action)
	if err != nil {
		return err
	}
	*rule = OutboundRule{ID: wire.ID, When: wire.When, Milestone: wire.Milestone, Action: action}
	return nil
}
