package jsonschema

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Validator struct{}

func (Validator) Validate(schema, value json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	var definition, instance any
	if err := json.Unmarshal(schema, &definition); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("workflow-value.json", definition); err != nil {
		return err
	}
	compiled, err := compiler.Compile("workflow-value.json")
	if err != nil {
		return err
	}
	if len(value) == 0 {
		return nil
	}
	if err := json.Unmarshal(value, &instance); err != nil {
		return err
	}
	return compiled.Validate(instance)
}
