package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"darkstar/src/ports/valueschema"
)

// Resource is a closed source union. Storage and execution still share the
// input/output declarations; canvas resource cards are projections of them.
type Resource struct{ Source ResourceSource }
type ResourceSource interface{ resourceSource() }
type ArtifactResource struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

func (ArtifactResource) resourceSource() {}

type TaskResource struct{}
type RepositoryResource struct{}
type TemplateResource struct {
	Content          string   `json:"content"`
	Version          string   `json:"version"`
	RequiredHeadings []string `json:"requiredHeadings,omitempty"`
}
type ConstantResource struct {
	Value json.RawMessage `json:"value"`
}
type ConfigResource struct {
	Key string `json:"key"`
}
type OpenItemsResource struct{}
type DecisionLogResource struct{}

func (TaskResource) resourceSource()        {}
func (RepositoryResource) resourceSource()  {}
func (TemplateResource) resourceSource()    {}
func (ConstantResource) resourceSource()    {}
func (ConfigResource) resourceSource()      {}
func (OpenItemsResource) resourceSource()   {}
func (DecisionLogResource) resourceSource() {}

func (r Resource) MarshalJSON() ([]byte, error) {
	kind := ""
	switch r.Source.(type) {
	case ArtifactResource:
		kind = "artifact"
	case TaskResource:
		kind = "task"
	case RepositoryResource:
		kind = "repository"
	case TemplateResource:
		kind = "template"
	case ConstantResource:
		kind = "constant"
	case ConfigResource:
		kind = "config"
	case OpenItemsResource:
		kind = "open_items"
	case DecisionLogResource:
		kind = "decision_log"
	default:
		return nil, errors.New("unsupported resource source")
	}
	b, err := json.Marshal(r.Source)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	fields["kind"], _ = json.Marshal(kind)
	return json.Marshal(fields)
}

func (r *Resource) UnmarshalJSON(b []byte) error {
	var fields map[string]json.RawMessage
	if err := strictDecode(b, &fields); err != nil {
		return err
	}
	var kind string
	if err := json.Unmarshal(fields["kind"], &kind); err != nil {
		return errors.New("resource kind is required")
	}
	delete(fields, "kind")
	body, _ := json.Marshal(fields)
	var source ResourceSource
	switch kind {
	case "artifact":
		var value ArtifactResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		if err := ValidateArtifact(ArtifactContract{Filename: value.Filename}, "placeholder", nil); err != nil {
			return err
		}
		source = value
	case "task":
		var value TaskResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		source = value
	case "repository":
		var value RepositoryResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		source = value
	case "template":
		var value TemplateResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.Content) == "" || !semanticVersionPattern.MatchString(value.Version) {
			return errors.New("template content and semantic version are required")
		}
		source = value
	case "constant":
		var value ConstantResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		if len(value.Value) == 0 {
			return errors.New("constant value is required")
		}
		source = value
	case "config":
		var value ConfigResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.Key) == "" {
			return errors.New("config key is required")
		}
		source = value
	case "open_items":
		var value OpenItemsResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		source = value
	case "decision_log":
		var value DecisionLogResource
		if err := strictDecode(body, &value); err != nil {
			return err
		}
		source = value
	default:
		return fmt.Errorf("unknown resource kind %q", kind)
	}
	r.Source = source
	return nil
}

// ArtifactContract identifies one independently submitted deliverable. A
// template names a node input, never an implicitly matched document title.
type ArtifactContract struct {
	Filename      string     `json:"filename"`
	TemplateInput Identifier `json:"templateInput,omitempty"`
}

func ValidateDeliverable(node Node, id Identifier, value json.RawMessage, inputs map[Identifier]json.RawMessage, validators ...valueschema.Validator) error {
	declaration, exists := node.Fields().Outputs[id]
	if !exists {
		return fmt.Errorf("undeclared output %q", id)
	}
	if !literalMatchesType(value, declaration.Type) {
		return fmt.Errorf("output %q must have type %s", id, declaration.Type)
	}
	if len(declaration.SchemaDefinition) > 0 {
		if len(validators) == 0 || validators[0] == nil {
			return errors.New("output requires a value schema validator")
		}
		if err := validators[0].Validate(declaration.SchemaDefinition, value); err != nil {
			return fmt.Errorf("output %s: %w", id, err)
		}
	}
	if declaration.Artifact == nil {
		return nil
	}
	var content string
	if err := json.Unmarshal(value, &content); err != nil {
		return err
	}
	var template *TemplateResource
	if inputID := declaration.Artifact.TemplateInput; inputID != "" {
		var resolved TemplateResource
		if err := json.Unmarshal(inputs[inputID], &resolved); err != nil || resolved.Content == "" {
			return fmt.Errorf("template input %q is unavailable", inputID)
		}
		template = &resolved
	}
	return ValidateArtifact(*declaration.Artifact, content, template)
}

// ValidateSchemaValue validates the transport shape. Full schema compilation and
// value checking belong to the injected valueschema port at catalog/execution boundaries.
func ValidateSchemaValue(schema, value json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	var definition any
	if err := json.Unmarshal(schema, &definition); err != nil {
		return err
	}
	switch definition.(type) {
	case map[string]any, bool:
	default:
		return errors.New("schema must be an object or boolean")
	}
	if len(value) > 0 && !json.Valid(value) {
		return errors.New("invalid JSON value")
	}
	return nil
}

func ValidateArtifact(contract ArtifactContract, content string, template *TemplateResource) error {
	if contract.Filename == "" || path.Base(contract.Filename) != contract.Filename || strings.ContainsAny(contract.Filename, "\\:") || contract.Filename == "." || contract.Filename == ".." {
		return errors.New("artifact filename must be a single filename")
	}
	if strings.TrimSpace(content) == "" {
		return errors.New("artifact content is empty")
	}
	if template != nil {
		headings := map[string]bool{}
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				headings[strings.TrimSpace(strings.TrimLeft(line, "#"))] = true
			}
		}
		for _, heading := range template.RequiredHeadings {
			if !headings[heading] {
				return fmt.Errorf("missing required heading %q", heading)
			}
		}
	}
	return nil
}

func (state *validationState) validateResources() {
	for id, declaration := range state.document.Spec.Inputs {
		location := fmt.Sprintf("/spec/inputs/%s", id)
		if err := ValidateSchemaValue(declaration.SchemaDefinition, nil); err != nil {
			state.add(ValidationSchemaInvalid, err.Error(), location+"/schemaDefinition", nil)
		}
		if declaration.Resource == nil {
			continue
		}
		want := declaration.Type
		switch source := declaration.Resource.Source.(type) {
		case TaskResource, RepositoryResource, TemplateResource, OpenItemsResource, DecisionLogResource:
			want = ValueObject
		case ArtifactResource:
			want = ValueString
		case ConstantResource:
			if !literalMatchesType(source.Value, declaration.Type) {
				state.add(ValidationBindingIncompatible, "constant does not match declared type", location, nil)
			}
			if err := ValidateSchemaValue(declaration.SchemaDefinition, source.Value); err != nil {
				state.add(ValidationSchemaInvalid, err.Error(), location, nil)
			}
		case ConfigResource:
		default:
			state.add(ValidationSchemaInvalid, "unknown resource source", location, nil)
		}
		if declaration.Type != want {
			state.add(ValidationBindingIncompatible, "resource requires an object value", location, nil)
		}
	}
	for nodeID, node := range state.nodes {
		files := map[string]bool{}
		for outputID, output := range node.Fields().Outputs {
			location := fmt.Sprintf("/spec/nodes/%s/outputs/%s", nodeID, outputID)
			if err := ValidateSchemaValue(output.SchemaDefinition, nil); err != nil {
				state.add(ValidationSchemaInvalid, err.Error(), location+"/schemaDefinition", nil)
			}
			if output.Artifact == nil {
				continue
			}
			if output.Type != ValueString {
				state.add(ValidationBindingIncompatible, "Markdown artifact output must be a string", location, nil)
			}
			if err := ValidateArtifact(*output.Artifact, "placeholder", nil); err != nil {
				state.add(ValidationSchemaInvalid, err.Error(), location+"/artifact", nil)
			}
			filename := strings.ToLower(output.Artifact.Filename)
			if files[filename] {
				state.add(ValidationSchemaInvalid, "artifact filenames must be distinct within a node", location, nil)
			}
			files[filename] = true
			if id := output.Artifact.TemplateInput; id != "" {
				binding, ok := node.Fields().Inputs[id]
				if !ok {
					state.add(ValidationReferenceMissing, "template input is missing", location, nil)
					continue
				}
				parts := strings.Split(binding.Source(), ".")
				if len(parts) != 3 || parts[0] != "run" {
					state.add(ValidationBindingIncompatible, "template input must bind a template resource", location, nil)
					continue
				}
				resource := state.document.Spec.Inputs[Identifier(parts[2])].Resource
				if resource == nil {
					state.add(ValidationBindingIncompatible, "template input must bind a template resource", location, nil)
					continue
				}
				if _, ok := resource.Source.(TemplateResource); !ok {
					state.add(ValidationBindingIncompatible, "template input must bind a template resource", location, nil)
				}
			}
		}
	}
}

func literalMatchesType(raw json.RawMessage, kind ValueType) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch kind {
	case ValueNull:
		return value == nil
	case ValueBoolean:
		_, ok := value.(bool)
		return ok
	case ValueString:
		_, ok := value.(string)
		return ok
	case ValueObject:
		_, ok := value.(map[string]any)
		return ok
	case ValueArray:
		_, ok := value.([]any)
		return ok
	case ValueNumber:
		_, ok := value.(float64)
		return ok
	case ValueInteger:
		n, ok := value.(float64)
		return ok && n == float64(int64(n))
	default:
		return false
	}
}
