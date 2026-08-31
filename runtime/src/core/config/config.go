// Package config resolves configuration layers without performing filesystem or
// environment access.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Scope identifies one position in DARKSTAR's configuration precedence.
type Scope uint8

const (
	ScopeDefault Scope = iota + 1
	ScopeUser
	ScopeProject
	ScopeRun
	ScopeCLI
)

func (s Scope) String() string {
	switch s {
	case ScopeDefault:
		return "default"
	case ScopeUser:
		return "user"
	case ScopeProject:
		return "project"
	case ScopeRun:
		return "run"
	case ScopeCLI:
		return "cli"
	default:
		return "unknown"
	}
}

func (s Scope) valid() bool {
	return s >= ScopeDefault && s <= ScopeCLI
}

// Source is an immutable attribution for a configuration value. Sources are
// built only by the layer constructors so a file scope cannot exist without an
// absolute filename and an override cannot masquerade as a file.
type Source struct {
	scope     Scope
	reference string
}

// Scope returns the precedence scope that supplied the value.
func (s Source) Scope() Scope { return s.scope }

// Reference returns the source filename or override identity.
func (s Source) Reference() string { return s.reference }

// MarshalJSON exposes source attribution without making Source mutable.
func (s Source) MarshalJSON() ([]byte, error) {
	value := struct {
		Scope     string `json:"scope"`
		Reference string `json:"reference,omitempty"`
	}{Scope: s.scope.String(), Reference: s.reference}
	return json.Marshal(value)
}

// Layer is one immutable configuration document and its source.
type Layer struct {
	source Source
	values map[string]any
}

// Defaults creates the required lowest-precedence shipped-default layer.
func Defaults(values map[string]any) (Layer, error) {
	return newLayer(Source{scope: ScopeDefault, reference: "shipped defaults"}, values)
}

// UserFile creates a user configuration layer from an absolute filename.
func UserFile(path string, values map[string]any) (Layer, error) {
	return fileLayer(ScopeUser, path, values)
}

// ProjectFile creates a project configuration layer from an absolute filename.
func ProjectFile(path string, values map[string]any) (Layer, error) {
	return fileLayer(ScopeProject, path, values)
}

// RunOverride creates a work-item or run override layer.
func RunOverride(reference string, values map[string]any) (Layer, error) {
	if strings.TrimSpace(reference) == "" {
		return Layer{}, errors.New("run override reference is required")
	}
	return newLayer(Source{scope: ScopeRun, reference: reference}, values)
}

// CLIOverride creates the highest-precedence command-line override layer.
func CLIOverride(values map[string]any) (Layer, error) {
	return newLayer(Source{scope: ScopeCLI, reference: "command line"}, values)
}

// Source returns the immutable source for this layer.
func (l Layer) Source() Source { return l.source }

func fileLayer(scope Scope, path string, values map[string]any) (Layer, error) {
	if !filepath.IsAbs(path) {
		return Layer{}, fmt.Errorf("%s configuration path must be absolute: %q", scope, path)
	}
	return newLayer(Source{scope: scope, reference: filepath.Clean(path)}, values)
}

func newLayer(source Source, values map[string]any) (Layer, error) {
	if !source.scope.valid() {
		return Layer{}, fmt.Errorf("invalid configuration scope %d", source.scope)
	}
	cloned, err := cloneMap(values)
	if err != nil {
		return Layer{}, err
	}
	return Layer{source: source, values: cloned}, nil
}

// Effective is an immutable resolved configuration. Values contains the merged
// tree while Sources identifies the winning layer for every leaf by JSON
// Pointer, for example /spec/provider/default.
type Effective struct {
	values  map[string]any
	sources map[string]Source
}

// ResolvedValue keeps a value and its winning source together.
type ResolvedValue struct {
	value  any
	source Source
}

// Value returns a defensive copy of the resolved value.
func (v ResolvedValue) Value() any {
	cloned, _ := cloneValue(v.value)
	return cloned
}

// Source returns the layer that supplied the resolved value.
func (v ResolvedValue) Source() Source { return v.source }

// Resolve merges the required shipped defaults with optional higher-precedence
// layers. Call order is irrelevant; duplicate scopes are rejected.
func Resolve(defaults Layer, overlays ...Layer) (Effective, error) {
	if defaults.source.scope != ScopeDefault {
		return Effective{}, errors.New("configuration resolution requires a shipped-default layer")
	}

	layers := append([]Layer{defaults}, overlays...)
	seen := make(map[Scope]struct{}, len(layers))
	for _, layer := range layers {
		if !layer.source.scope.valid() {
			return Effective{}, errors.New("configuration layer was not created by a layer constructor")
		}
		if _, exists := seen[layer.source.scope]; exists {
			return Effective{}, fmt.Errorf("duplicate %s configuration layer", layer.source.scope)
		}
		seen[layer.source.scope] = struct{}{}
	}
	sort.Slice(layers, func(i, j int) bool {
		return layers[i].source.scope < layers[j].source.scope
	})

	effective := Effective{
		values:  make(map[string]any),
		sources: make(map[string]Source),
	}
	for _, layer := range layers {
		mergeMap(effective.values, layer.values, layer.source, nil, effective.sources)
	}
	return effective, nil
}

// Values returns a defensive copy of the merged configuration tree.
func (e Effective) Values() map[string]any {
	cloned, _ := cloneMap(e.values)
	return cloned
}

// Sources returns a copy of the JSON-Pointer-to-source attribution map.
func (e Effective) Sources() map[string]Source {
	result := make(map[string]Source, len(e.sources))
	for path, source := range e.sources {
		result[path] = source
	}
	return result
}

// Lookup returns a resolved leaf or collection at the supplied path segments.
// Collection lookups use the most specific winning source below that path only
// when every descendant came from the same layer.
func (e Effective) Lookup(path ...string) (ResolvedValue, bool) {
	if len(path) == 0 {
		return ResolvedValue{}, false
	}
	var current any = e.values
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ResolvedValue{}, false
		}
		current, ok = object[segment]
		if !ok {
			return ResolvedValue{}, false
		}
	}

	pointer := jsonPointer(path)
	if source, ok := e.sources[pointer]; ok {
		return ResolvedValue{value: current, source: source}, true
	}
	var source Source
	found := false
	for candidate, candidateSource := range e.sources {
		if strings.HasPrefix(candidate, pointer+"/") {
			if found && candidateSource != source {
				return ResolvedValue{}, false
			}
			source = candidateSource
			found = true
		}
	}
	if !found {
		return ResolvedValue{}, false
	}
	return ResolvedValue{value: current, source: source}, true
}

func mergeMap(destination, incoming map[string]any, source Source, path []string, sources map[string]Source) {
	for key, incomingValue := range incoming {
		childPath := appendPath(path, key)
		pointer := jsonPointer(childPath)
		incomingMap, isMap := incomingValue.(map[string]any)
		if isMap && len(incomingMap) > 0 {
			destinationMap, destinationIsMap := destination[key].(map[string]any)
			if !destinationIsMap {
				destinationMap = make(map[string]any)
				destination[key] = destinationMap
				deleteSourcesAtOrBelow(sources, pointer)
			}
			mergeMap(destinationMap, incomingMap, source, childPath, sources)
			continue
		}

		cloned, _ := cloneValue(incomingValue)
		destination[key] = cloned
		deleteSourcesAtOrBelow(sources, pointer)
		sources[pointer] = source
	}
}

func deleteSourcesAtOrBelow(sources map[string]Source, pointer string) {
	for candidate := range sources {
		if candidate == pointer || strings.HasPrefix(candidate, pointer+"/") {
			delete(sources, candidate)
		}
	}
}

func appendPath(path []string, segment string) []string {
	result := make([]string, len(path)+1)
	copy(result, path)
	result[len(path)] = segment
	return result
}

func jsonPointer(path []string) string {
	encoded := make([]string, len(path))
	for index, segment := range path {
		segment = strings.ReplaceAll(segment, "~", "~0")
		encoded[index] = strings.ReplaceAll(segment, "/", "~1")
	}
	return "/" + strings.Join(encoded, "/")
}

func cloneMap(values map[string]any) (map[string]any, error) {
	if values == nil {
		return make(map[string]any), nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		if key == "" {
			return nil, errors.New("configuration keys must not be empty")
		}
		cloned, err := cloneValue(value)
		if err != nil {
			return nil, fmt.Errorf("configuration key %q: %w", key, err)
		}
		result[key] = cloned
	}
	return result, nil
}

func cloneValue(value any) (any, error) {
	switch value := value.(type) {
	case nil, string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return value, nil
	case map[string]any:
		return cloneMap(value)
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			cloned, err := cloneValue(item)
			if err != nil {
				return nil, fmt.Errorf("array item %d: %w", index, err)
			}
			result[index] = cloned
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", value)
	}
}
