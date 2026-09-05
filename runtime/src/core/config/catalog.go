package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// SettingType is the closed set of values accepted by the mutation API.
type SettingType string

const (
	SettingString          SettingType = "string"
	SettingInteger         SettingType = "integer"
	SettingBoolean         SettingType = "boolean"
	SettingEnum            SettingType = "enum"
	SettingPath            SettingType = "path"
	SettingSecretReference SettingType = "secret_reference"
)

// Sensitivity determines which mutation surface may carry a setting's value.
type Sensitivity string

const (
	SensitivityPublic Sensitivity = "public"
	SensitivitySecret Sensitivity = "secret"
)

// RestartImpact is the precise runtime action required after a successful write.
type RestartImpact string

const (
	RestartNone   RestartImpact = "none"
	RestartDaemon RestartImpact = "daemon"
)

// MutationScopeKind is the only ordinary configuration scope that can be edited.
type MutationScopeKind string

const (
	MutationScopeUser    MutationScopeKind = "user"
	MutationScopeProject MutationScopeKind = "project"
)

// SettingAction is an explicit capability, not an inference from health prose.
type SettingAction string

const (
	SettingActionPreview SettingAction = "preview"
	SettingActionApply   SettingAction = "apply"
	SettingActionRestore SettingAction = "restore"
)

// Constraints carries only constraints meaningful for a descriptor's type.
type Constraints struct {
	Required      bool     `json:"required"`
	Absolute      bool     `json:"absolute,omitempty"`
	ExistingFile  bool     `json:"existingFile,omitempty"`
	Minimum       *int64   `json:"minimum,omitempty"`
	Maximum       *int64   `json:"maximum,omitempty"`
	AllowedValues []string `json:"allowedValues,omitempty"`
}

// TypedValue is a tagged union. The payload is private so callers cannot build
// a value whose discriminator disagrees with its representation.
type TypedValue struct{ variant typedValueVariant }

type typedValueVariant interface {
	settingType() SettingType
	value() any
}

type stringVariant struct {
	kind SettingType
	text string
}
type integerVariant int64
type booleanVariant bool

func (v stringVariant) settingType() SettingType { return v.kind }
func (v stringVariant) value() any               { return v.text }
func (integerVariant) settingType() SettingType  { return SettingInteger }
func (v integerVariant) value() any              { return int64(v) }
func (booleanVariant) settingType() SettingType  { return SettingBoolean }
func (v booleanVariant) value() any              { return bool(v) }

func StringValue(value string) TypedValue {
	return TypedValue{variant: stringVariant{kind: SettingString, text: value}}
}
func IntegerValue(value int64) TypedValue { return TypedValue{variant: integerVariant(value)} }
func BooleanValue(value bool) TypedValue  { return TypedValue{variant: booleanVariant(value)} }
func EnumValue(value string) TypedValue {
	return TypedValue{variant: stringVariant{kind: SettingEnum, text: value}}
}
func PathValue(value string) TypedValue {
	return TypedValue{variant: stringVariant{kind: SettingPath, text: value}}
}
func SecretReferenceValue(value string) TypedValue {
	return TypedValue{variant: stringVariant{kind: SettingSecretReference, text: value}}
}

func (v TypedValue) Type() SettingType {
	if v.variant == nil {
		return ""
	}
	return v.variant.settingType()
}

// Value returns the primitive representation safe for ordinary configuration.
func (v TypedValue) Value() any {
	if v.variant == nil {
		return nil
	}
	return v.variant.value()
}

func (v TypedValue) MarshalJSON() ([]byte, error) {
	if v.variant == nil {
		return nil, errors.New("typed configuration value is required")
	}
	return json.Marshal(struct {
		Type  SettingType `json:"type"`
		Value any         `json:"value"`
	}{Type: v.Type(), Value: v.Value()})
}

func (v *TypedValue) UnmarshalJSON(encoded []byte) error {
	var envelope struct {
		Type  SettingType     `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("typed value must contain one JSON object")
	}
	switch envelope.Type {
	case SettingString, SettingEnum, SettingPath, SettingSecretReference:
		var value string
		if err := json.Unmarshal(envelope.Value, &value); err != nil {
			return fmt.Errorf("%s value must be a string", envelope.Type)
		}
		v.variant = stringVariant{kind: envelope.Type, text: value}
	case SettingInteger:
		var value int64
		if err := json.Unmarshal(envelope.Value, &value); err != nil {
			return errors.New("integer value must be an integer")
		}
		v.variant = integerVariant(value)
	case SettingBoolean:
		var value bool
		if err := json.Unmarshal(envelope.Value, &value); err != nil {
			return errors.New("boolean value must be a boolean")
		}
		v.variant = booleanVariant(value)
	default:
		return fmt.Errorf("unsupported configuration value type %q", envelope.Type)
	}
	return nil
}

// MutationScope is a tagged union: user has no project identity while project requires one.
type MutationScope struct{ variant mutationScopeVariant }
type mutationScopeVariant interface {
	kind() MutationScopeKind
	projectID() string
}
type userScope struct{}
type projectScope string

func (userScope) kind() MutationScopeKind    { return MutationScopeUser }
func (userScope) projectID() string          { return "" }
func (projectScope) kind() MutationScopeKind { return MutationScopeProject }
func (v projectScope) projectID() string     { return string(v) }
func UserMutationScope() MutationScope       { return MutationScope{variant: userScope{}} }
func ProjectMutationScope(projectID string) (MutationScope, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return MutationScope{}, errors.New("project scope requires projectId")
	}
	return MutationScope{variant: projectScope(projectID)}, nil
}
func (s MutationScope) Kind() MutationScopeKind {
	if s.variant == nil {
		return ""
	}
	return s.variant.kind()
}
func (s MutationScope) ProjectID() string {
	if s.variant == nil {
		return ""
	}
	return s.variant.projectID()
}
func (s MutationScope) MarshalJSON() ([]byte, error) {
	switch s.Kind() {
	case MutationScopeUser:
		return []byte(`{"type":"user"}`), nil
	case MutationScopeProject:
		return json.Marshal(struct {
			Type      MutationScopeKind `json:"type"`
			ProjectID string            `json:"projectId"`
		}{s.Kind(), s.ProjectID()})
	default:
		return nil, errors.New("configuration mutation scope is required")
	}
}
func (s *MutationScope) UnmarshalJSON(encoded []byte) error {
	var header struct {
		Type MutationScopeKind `json:"type"`
	}
	if err := json.Unmarshal(encoded, &header); err != nil {
		return err
	}
	switch header.Type {
	case MutationScopeUser:
		var exact struct {
			Type MutationScopeKind `json:"type"`
		}
		if err := strictJSON(encoded, &exact); err != nil {
			return err
		}
		*s = UserMutationScope()
	case MutationScopeProject:
		var exact struct {
			Type      MutationScopeKind `json:"type"`
			ProjectID string            `json:"projectId"`
		}
		if err := strictJSON(encoded, &exact); err != nil {
			return err
		}
		value, err := ProjectMutationScope(exact.ProjectID)
		if err != nil {
			return err
		}
		*s = value
	default:
		return fmt.Errorf("unsupported configuration scope %q", header.Type)
	}
	return nil
}

func strictJSON(encoded []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("expected one JSON object")
	}
	return nil
}

// SettingDescriptor is one stable entry in the supported mutation catalog.
type SettingDescriptor struct {
	Key           string              `json:"key"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	Type          SettingType         `json:"type"`
	Default       TypedValue          `json:"default"`
	Constraints   Constraints         `json:"constraints"`
	Sensitivity   Sensitivity         `json:"sensitivity"`
	AllowedScopes []MutationScopeKind `json:"allowedScopes"`
	Restart       RestartImpact       `json:"restart"`
	Actions       []SettingAction     `json:"actions"`
}

type Catalog struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Settings      []SettingDescriptor `json:"settings"`
}

var supportedSettings = []SettingDescriptor{
	{
		Key: "provider.codex.actionAvailability", Title: "Codex action availability", Description: "Controls whether workflows may select Codex actions for this scope.",
		Type: SettingEnum, Default: EnumValue("enabled"), Constraints: Constraints{Required: true, AllowedValues: []string{"enabled", "disabled"}},
		Sensitivity: SensitivityPublic, AllowedScopes: []MutationScopeKind{MutationScopeUser, MutationScopeProject}, Restart: RestartDaemon,
		Actions: []SettingAction{SettingActionPreview, SettingActionApply, SettingActionRestore},
	},
	{
		Key: "provider.codex.executable", Title: "Codex executable", Description: "Absolute path to the trusted Codex executable used by the provider.",
		Type: SettingPath, Default: PathValue("codex"), Constraints: Constraints{Required: true, Absolute: true, ExistingFile: true},
		Sensitivity: SensitivityPublic, AllowedScopes: []MutationScopeKind{MutationScopeUser}, Restart: RestartDaemon,
		Actions: []SettingAction{SettingActionPreview, SettingActionApply, SettingActionRestore},
	},
	{
		Key: "provider.codex.apiKey", Title: "Codex API key", Description: "Reference to a user-owned secret; secret material is written only through the secret endpoint.",
		Type: SettingSecretReference, Default: SecretReferenceValue("codex-api-key"), Constraints: Constraints{Required: true},
		Sensitivity: SensitivitySecret, AllowedScopes: []MutationScopeKind{MutationScopeUser}, Restart: RestartDaemon,
		Actions: []SettingAction{SettingActionPreview, SettingActionApply, SettingActionRestore},
	},
}

func SupportedCatalog() Catalog {
	settings := append([]SettingDescriptor(nil), supportedSettings...)
	return Catalog{SchemaVersion: 1, Settings: settings}
}

func LookupSetting(key string) (SettingDescriptor, bool) {
	for _, setting := range supportedSettings {
		if setting.Key == key {
			return setting, true
		}
	}
	return SettingDescriptor{}, false
}

// ValidateValue applies the descriptor's type and concrete constraints.
func (d SettingDescriptor) ValidateValue(value TypedValue) error {
	if value.Type() != d.Type {
		return fmt.Errorf("setting %s requires type %s, got %s", d.Key, d.Type, value.Type())
	}
	switch d.Type {
	case SettingString, SettingEnum, SettingPath, SettingSecretReference:
		text := value.Value().(string)
		if d.Constraints.Required && strings.TrimSpace(text) == "" {
			return fmt.Errorf("setting %s is required", d.Key)
		}
		if d.Constraints.Absolute && !filepath.IsAbs(text) {
			return fmt.Errorf("setting %s must be an absolute path", d.Key)
		}
		if len(d.Constraints.AllowedValues) != 0 {
			found := false
			for _, allowed := range d.Constraints.AllowedValues {
				if text == allowed {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("setting %s must be one of %s", d.Key, strings.Join(d.Constraints.AllowedValues, ", "))
			}
		}
	case SettingInteger:
		number := value.Value().(int64)
		if d.Constraints.Minimum != nil && number < *d.Constraints.Minimum {
			return fmt.Errorf("setting %s must be at least %d", d.Key, *d.Constraints.Minimum)
		}
		if d.Constraints.Maximum != nil && number > *d.Constraints.Maximum {
			return fmt.Errorf("setting %s must be at most %d", d.Key, *d.Constraints.Maximum)
		}
	case SettingBoolean:
	default:
		return fmt.Errorf("setting %s has unsupported descriptor type %s", d.Key, d.Type)
	}
	return nil
}
