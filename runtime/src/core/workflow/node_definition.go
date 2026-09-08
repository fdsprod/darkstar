package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNodeDefinitionNotFound        = errors.New("node definition not found")
	ErrNodeDefinitionImmutable       = errors.New("built-in node definition is immutable")
	ErrNodeDefinitionVersionConflict = errors.New("node definition version conflict")
)

type NodeImplementationKind string

const (
	NodeImplementationExecutor NodeImplementationKind = "executor"
	NodeImplementationRouting  NodeImplementationKind = "routing"
)

type NodeDefinitionLifecycle string

const (
	NodeDefinitionActive   NodeDefinitionLifecycle = "active"
	NodeDefinitionArchived NodeDefinitionLifecycle = "archived"
)

// NodeDefinition is one immutable reusable-node version. Usage is intentionally
// absent: it is derived from workflow references by the projection layer.
type NodeDefinition struct {
	Ref                  ResolvedNodeDefinitionRef        `json:"ref"`
	DisplayName          string                           `json:"displayName"`
	Description          string                           `json:"description"`
	Compatibility        string                           `json:"compatibility"`
	Inputs               map[Identifier]ValueDeclaration  `json:"inputs"`
	Outputs              map[Identifier]OutputDeclaration `json:"outputs"`
	ConfigurationSchema  json.RawMessage                  `json:"configurationSchema"`
	Implementation       NodeImplementationKind           `json:"implementation"`
	RequiredCapabilities []CapabilityReference            `json:"requiredCapabilities"`
	Lifecycle            NodeDefinitionLifecycle          `json:"lifecycle"`
	DerivedFrom          *ResolvedNodeDefinitionRef       `json:"derivedFrom,omitempty"`
	Usage                []NodeDefinitionUsage            `json:"usage,omitempty"`
	CreatedAt            time.Time                        `json:"createdAt"`
}

func (value *NodeDefinition) UnmarshalJSON(data []byte) error {
	type wire struct {
		Ref                  json.RawMessage                  `json:"ref"`
		DisplayName          string                           `json:"displayName"`
		Description          string                           `json:"description"`
		Compatibility        string                           `json:"compatibility"`
		Inputs               map[Identifier]ValueDeclaration  `json:"inputs"`
		Outputs              map[Identifier]OutputDeclaration `json:"outputs"`
		ConfigurationSchema  json.RawMessage                  `json:"configurationSchema"`
		Implementation       NodeImplementationKind           `json:"implementation"`
		RequiredCapabilities []CapabilityReference            `json:"requiredCapabilities"`
		Lifecycle            NodeDefinitionLifecycle          `json:"lifecycle"`
		DerivedFrom          json.RawMessage                  `json:"derivedFrom"`
		CreatedAt            time.Time                        `json:"createdAt"`
	}
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	ref, err := decodeResolvedNodeDefinitionRef(decoded.Ref)
	if err != nil || ref == nil {
		return errors.New("node definition ref is required")
	}
	var derived *ResolvedNodeDefinitionRef
	if len(decoded.DerivedFrom) > 0 && string(decoded.DerivedFrom) != "null" {
		derived, err = decodeResolvedNodeDefinitionRef(decoded.DerivedFrom)
		if err != nil {
			return err
		}
	}
	*value = NodeDefinition{Ref: *ref, DisplayName: decoded.DisplayName, Description: decoded.Description, Compatibility: decoded.Compatibility, Inputs: decoded.Inputs, Outputs: decoded.Outputs, ConfigurationSchema: decoded.ConfigurationSchema, Implementation: decoded.Implementation, RequiredCapabilities: decoded.RequiredCapabilities, Lifecycle: decoded.Lifecycle, DerivedFrom: derived, CreatedAt: decoded.CreatedAt}
	return nil
}

type NodeDefinitionFilter struct {
	Query     string
	Scope     *NodeDefinitionScope
	Lifecycle *NodeDefinitionLifecycle
}

type NodeDefinitionUsage struct {
	Workflow WorkflowIdentity `json:"workflow"`
	NodeIDs  []Identifier     `json:"nodeIds"`
}

// DefinitionUsage derives usage from immutable workflow documents, avoiding a
// mutable counter that could drift from the references it summarizes.
func DefinitionUsage(ref ResolvedNodeDefinitionRef, definitions []Definition) []NodeDefinitionUsage {
	result := []NodeDefinitionUsage{}
	for _, installed := range definitions {
		ids := []Identifier{}
		for id, node := range installed.Document.Spec.Nodes {
			candidate := node.Fields().Definition
			if candidate != nil && nodeDefinitionKey(candidate.Ref) == nodeDefinitionKey(ref.Ref) && candidate.Digest == ref.Digest {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			result = append(result, NodeDefinitionUsage{Workflow: WorkflowIdentity{Name: installed.Version.Name, Version: installed.Version.Version, Digest: installed.Version.Digest}, NodeIDs: ids})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Workflow.Name < result[j].Workflow.Name || result[i].Workflow.Name == result[j].Workflow.Name && result[i].Workflow.Version < result[j].Workflow.Version
	})
	return result
}

// NodeDefinitionLibrary owns immutable versions. Publishing a new version or a
// derivative appends a record; archiving changes discovery only and never
// rewrites workflows that already captured an exact reference.
type NodeDefinitionLibrary struct {
	mu       sync.RWMutex
	versions map[string]NodeDefinition
}

func NewNodeDefinitionLibrary(builtins ...NodeDefinition) (*NodeDefinitionLibrary, error) {
	library := &NodeDefinitionLibrary{versions: map[string]NodeDefinition{}}
	for _, definition := range builtins {
		if definition.Ref.Ref == nil || definition.Ref.Ref.definitionScope() != NodeDefinitionBuiltIn {
			return nil, errors.New("initial node definitions must be built-ins")
		}
		if _, err := library.put(definition); err != nil {
			return nil, err
		}
	}
	return library, nil
}

func (library *NodeDefinitionLibrary) Publish(definition NodeDefinition) (NodeDefinition, error) {
	library.mu.Lock()
	defer library.mu.Unlock()
	if definition.Ref.Ref != nil && definition.Ref.Ref.definitionScope() == NodeDefinitionBuiltIn {
		return NodeDefinition{}, ErrNodeDefinitionImmutable
	}
	stored, err := library.put(definition)
	if err != nil {
		return NodeDefinition{}, err
	}
	return cloneNodeDefinition(stored), nil
}

func (library *NodeDefinitionLibrary) Duplicate(source ResolvedNodeDefinitionRef, target NodeDefinitionRef, now time.Time) (NodeDefinition, error) {
	library.mu.Lock()
	defer library.mu.Unlock()
	value, ok := library.versions[nodeDefinitionKey(source.Ref)]
	if !ok || value.Ref.Digest != source.Digest {
		return NodeDefinition{}, ErrNodeDefinitionNotFound
	}
	if target == nil || target.definitionScope() == NodeDefinitionBuiltIn {
		return NodeDefinition{}, ErrNodeDefinitionImmutable
	}
	value.Ref = ResolvedNodeDefinitionRef{Ref: target}
	value.DerivedFrom = &source
	value.Lifecycle = NodeDefinitionActive
	value.CreatedAt = now.UTC().Round(0)
	value.Ref.Digest = nodeDefinitionDigest(value)
	stored, err := library.put(value)
	if err != nil {
		return NodeDefinition{}, err
	}
	return cloneNodeDefinition(stored), nil
}

// Version copies an editable definition contract to a new semantic version in
// the same scope. The prior version remains independently resolvable.
func (library *NodeDefinitionLibrary) Version(source ResolvedNodeDefinitionRef, version string, now time.Time) (NodeDefinition, error) {
	if source.Ref == nil || !semanticVersionPattern.MatchString(version) {
		return NodeDefinition{}, errors.New("source and semantic target version are required")
	}
	var target NodeDefinitionRef
	switch ref := source.Ref.(type) {
	case ProjectNodeDefinitionRef:
		target = ProjectNodeDefinitionRef{ProjectID: ref.ProjectID, Name: ref.Name, Version: version}
	case UserNodeDefinitionRef:
		target = UserNodeDefinitionRef{UserID: ref.UserID, Name: ref.Name, Version: version}
	case BuiltInNodeDefinitionRef:
		return NodeDefinition{}, ErrNodeDefinitionImmutable
	default:
		return NodeDefinition{}, fmt.Errorf("unsupported node-definition ref %T", source.Ref)
	}
	return library.Duplicate(source, target, now)
}

func (library *NodeDefinitionLibrary) Archive(ref ResolvedNodeDefinitionRef) (NodeDefinition, error) {
	library.mu.Lock()
	defer library.mu.Unlock()
	if ref.Ref == nil || ref.Ref.definitionScope() == NodeDefinitionBuiltIn {
		return NodeDefinition{}, ErrNodeDefinitionImmutable
	}
	key := nodeDefinitionKey(ref.Ref)
	value, ok := library.versions[key]
	if !ok || value.Ref.Digest != ref.Digest {
		return NodeDefinition{}, ErrNodeDefinitionNotFound
	}
	value.Lifecycle = NodeDefinitionArchived
	library.versions[key] = value
	return cloneNodeDefinition(value), nil
}

func (library *NodeDefinitionLibrary) Resolve(ref ResolvedNodeDefinitionRef) (NodeDefinition, error) {
	library.mu.RLock()
	defer library.mu.RUnlock()
	value, ok := library.versions[nodeDefinitionKey(ref.Ref)]
	if !ok || value.Ref.Digest != ref.Digest {
		return NodeDefinition{}, ErrNodeDefinitionNotFound
	}
	return cloneNodeDefinition(value), nil
}

func (library *NodeDefinitionLibrary) Search(filter NodeDefinitionFilter) []NodeDefinition {
	library.mu.RLock()
	defer library.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	values := []NodeDefinition{}
	for _, value := range library.versions {
		if filter.Scope != nil && value.Ref.Ref.definitionScope() != *filter.Scope {
			continue
		}
		if filter.Lifecycle != nil && value.Lifecycle != *filter.Lifecycle {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(value.Ref.Ref.DefinitionName()+" "+value.DisplayName+" "+value.Description), query) {
			continue
		}
		values = append(values, cloneNodeDefinition(value))
	}
	sort.Slice(values, func(i, j int) bool {
		return nodeDefinitionKey(values[i].Ref.Ref) < nodeDefinitionKey(values[j].Ref.Ref)
	})
	return values
}

func (library *NodeDefinitionLibrary) put(value NodeDefinition) (NodeDefinition, error) {
	var err error
	value, err = sealNodeDefinition(value)
	if err != nil {
		return NodeDefinition{}, err
	}
	key := nodeDefinitionKey(value.Ref.Ref)
	if existing, ok := library.versions[key]; ok {
		if existing.Ref.Digest == value.Ref.Digest {
			return existing, nil
		}
		return NodeDefinition{}, ErrNodeDefinitionVersionConflict
	}
	library.versions[key] = cloneNodeDefinition(value)
	return value, nil
}

func sealNodeDefinition(value NodeDefinition) (NodeDefinition, error) {
	if value.Ref.Ref == nil || !workflowNamePattern.MatchString(value.Ref.Ref.DefinitionName()) || !semanticVersionPattern.MatchString(value.Ref.Ref.DefinitionVersion()) {
		return NodeDefinition{}, errors.New("node definition requires scoped name and semantic version")
	}
	if strings.TrimSpace(value.DisplayName) == "" || strings.TrimSpace(value.Compatibility) == "" || len(value.ConfigurationSchema) == 0 || !json.Valid(value.ConfigurationSchema) {
		return NodeDefinition{}, errors.New("node definition requires display name, compatibility, and configuration schema")
	}
	if value.Implementation != NodeImplementationExecutor && value.Implementation != NodeImplementationRouting {
		return NodeDefinition{}, fmt.Errorf("unsupported node definition implementation %q", value.Implementation)
	}
	if value.Lifecycle != NodeDefinitionActive && value.Lifecycle != NodeDefinitionArchived {
		return NodeDefinition{}, fmt.Errorf("unsupported node definition lifecycle %q", value.Lifecycle)
	}
	expected := nodeDefinitionDigest(value)
	if value.Ref.Digest == "" {
		value.Ref.Digest = expected
	}
	if value.Ref.Digest != expected {
		return NodeDefinition{}, errors.New("node definition digest does not match its immutable contract")
	}
	return value, nil
}

func nodeDefinitionDigest(value NodeDefinition) string {
	copy := value
	copy.Ref.Digest = ""
	copy.Lifecycle = ""
	copy.Usage = nil
	copy.CreatedAt = time.Time{}
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func nodeDefinitionKey(ref NodeDefinitionRef) string {
	if ref == nil {
		return ""
	}
	owner := ""
	switch value := ref.(type) {
	case ProjectNodeDefinitionRef:
		owner = value.ProjectID
	case UserNodeDefinitionRef:
		owner = value.UserID
	}
	return string(ref.definitionScope()) + "\x00" + owner + "\x00" + ref.DefinitionName() + "\x00" + ref.DefinitionVersion()
}

func cloneNodeDefinition(value NodeDefinition) NodeDefinition {
	clone := value
	clone.Ref = ResolvedNodeDefinitionRef{Ref: value.Ref.Ref, Digest: value.Ref.Digest}
	clone.Inputs = make(map[Identifier]ValueDeclaration, len(value.Inputs))
	for key, item := range value.Inputs {
		clone.Inputs[key] = item
	}
	clone.Outputs = make(map[Identifier]OutputDeclaration, len(value.Outputs))
	for key, item := range value.Outputs {
		clone.Outputs[key] = item
	}
	clone.ConfigurationSchema = append(json.RawMessage(nil), value.ConfigurationSchema...)
	if value.RequiredCapabilities != nil {
		clone.RequiredCapabilities = append(make([]CapabilityReference, 0, len(value.RequiredCapabilities)), value.RequiredCapabilities...)
	}
	if value.DerivedFrom != nil {
		copied := ResolvedNodeDefinitionRef{Ref: value.DerivedFrom.Ref, Digest: value.DerivedFrom.Digest}
		clone.DerivedFrom = &copied
	}
	clone.Usage = append([]NodeDefinitionUsage(nil), value.Usage...)
	return clone
}
