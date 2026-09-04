// Package config resolves configuration layers without performing filesystem or
// environment access.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// EffectiveReportSchemaVersion is the strict public projection version.
const EffectiveReportSchemaVersion = 1

// ValueKind identifies how an effective configuration value is represented.
type ValueKind string

const (
	ValueString   ValueKind = "string"
	ValueNumber   ValueKind = "number"
	ValueBoolean  ValueKind = "boolean"
	ValueNull     ValueKind = "null"
	ValueJSON     ValueKind = "json"
	ValueRedacted ValueKind = "redacted"
)

// FileScope is the closed set of ordinary configuration-file scopes.
type FileScope string

const (
	FileScopeUser    FileScope = "user"
	FileScopeProject FileScope = "project"
)

// File identifies one ordinary configuration file location. Secret files are
// intentionally not representable in this response.
type File struct {
	Scope FileScope `json:"scope"`
	Path  string    `json:"path"`
}

// DisplayValue is the safe, exact textual representation of a resolved value.
type DisplayValue struct {
	Kind    ValueKind `json:"kind"`
	Display string    `json:"display"`
}

// Entry is one effective value with its winning source attribution.
type Entry struct {
	Path   string       `json:"path"`
	Value  DisplayValue `json:"value"`
	Source Source       `json:"source"`
}

// EffectiveReport is the read-only, redacted public configuration projection.
type EffectiveReport struct {
	SchemaVersion int     `json:"schemaVersion"`
	ProjectRoot   string  `json:"projectRoot"`
	Files         []File  `json:"files"`
	Entries       []Entry `json:"entries"`
}

var (
	secretTextPattern = regexp.MustCompile(`(?i)(?:\bbearer\s+\S+|-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:authorization|password|passphrase|secret|token|api[_-]?key|credential|client[_-]?secret|private[_-]?key)\b\s*[:=]\s*\S+)`)
	jwtPattern        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	credentialPattern = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://[^/@\s:]+:[^/@\s]+@|\b(?:github_pat_|gh[pousr]_|sk-)[A-Za-z0-9_-]{12,})`)
)

// NewEffectiveReport constructs a stable, path-sorted projection. It rejects
// file descriptors outside ordinary user/project configuration so callers
// cannot accidentally disclose a secrets-file location through this shape.
func NewEffectiveReport(projectRoot string, files []File, effective Effective) (EffectiveReport, error) {
	if !filepath.IsAbs(projectRoot) {
		return EffectiveReport{}, fmt.Errorf("project root must be absolute: %q", projectRoot)
	}
	if len(files) != 2 {
		return EffectiveReport{}, errors.New("effective configuration requires user and project file locations")
	}
	report := EffectiveReport{
		SchemaVersion: EffectiveReportSchemaVersion,
		ProjectRoot:   filepath.Clean(projectRoot),
		Files:         make([]File, len(files)),
		Entries:       make([]Entry, 0, len(effective.sources)),
	}
	seenFileScopes := make(map[FileScope]bool, len(files))
	for index, file := range files {
		if file.Scope != FileScopeUser && file.Scope != FileScopeProject {
			return EffectiveReport{}, fmt.Errorf("configuration file scope must be user or project, got %q", file.Scope)
		}
		if seenFileScopes[file.Scope] {
			return EffectiveReport{}, fmt.Errorf("duplicate %s configuration file", file.Scope)
		}
		seenFileScopes[file.Scope] = true
		if !filepath.IsAbs(file.Path) {
			return EffectiveReport{}, fmt.Errorf("%s configuration path must be absolute: %q", file.Scope, file.Path)
		}
		report.Files[index] = File{Scope: file.Scope, Path: filepath.Clean(file.Path)}
	}
	for pointer, source := range effective.sources {
		value, found := valueAtPointer(effective.values, pointer)
		if !found {
			return EffectiveReport{}, fmt.Errorf("configuration source path %q has no value", pointer)
		}
		display, err := safeDisplay(pointer, value)
		if err != nil {
			return EffectiveReport{}, fmt.Errorf("display configuration value %q: %w", pointer, err)
		}
		report.Entries = append(report.Entries, Entry{Path: pointer, Value: display, Source: source})
	}
	sort.Slice(report.Entries, func(i, j int) bool { return report.Entries[i].Path < report.Entries[j].Path })
	return report, nil
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

func valueAtPointer(values map[string]any, pointer string) (any, bool) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	var current any = values
	for _, encoded := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func safeDisplay(pointer string, value any) (DisplayValue, error) {
	if secretPath(pointer) || secretValue(value) {
		return DisplayValue{Kind: ValueRedacted, Display: "[redacted]"}, nil
	}
	switch value := value.(type) {
	case nil:
		return DisplayValue{Kind: ValueNull, Display: "null"}, nil
	case string:
		return DisplayValue{Kind: ValueString, Display: value}, nil
	case bool:
		return DisplayValue{Kind: ValueBoolean, Display: strconv.FormatBool(value)}, nil
	case int:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatInt(int64(value), 10)}, nil
	case int8:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatInt(int64(value), 10)}, nil
	case int16:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatInt(int64(value), 10)}, nil
	case int32:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatInt(int64(value), 10)}, nil
	case int64:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatInt(value, 10)}, nil
	case uint:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatUint(uint64(value), 10)}, nil
	case uint8:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatUint(uint64(value), 10)}, nil
	case uint16:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatUint(uint64(value), 10)}, nil
	case uint32:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatUint(uint64(value), 10)}, nil
	case uint64:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatUint(value, 10)}, nil
	case float32:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatFloat(float64(value), 'g', -1, 32)}, nil
	case float64:
		return DisplayValue{Kind: ValueNumber, Display: strconv.FormatFloat(value, 'g', -1, 64)}, nil
	case map[string]any, []any:
		encoded, err := json.Marshal(value)
		if err != nil {
			return DisplayValue{}, err
		}
		return DisplayValue{Kind: ValueJSON, Display: string(encoded)}, nil
	default:
		return DisplayValue{}, fmt.Errorf("unsupported value type %T", value)
	}
}

func secretPath(pointer string) bool {
	for _, encoded := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		if secretKey(segment) {
			return true
		}
	}
	return false
}

func secretKey(key string) bool {
	normalized := strings.ToLower(key)
	normalized = strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(normalized)
	switch normalized {
	case "authorization", "cookie", "setcookie", "password", "passphrase", "secret", "secrets", "token", "accesstoken", "refreshtoken", "apikey", "credential", "credentials", "clientsecret", "privatekey":
		return true
	default:
		// Effective configuration is a display surface, so prefer conservative
		// withholding over leaking an opaque credential under a compound name.
		return strings.Contains(normalized, "password") || strings.Contains(normalized, "passphrase") ||
			strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") ||
			strings.Contains(normalized, "credential") || strings.Contains(normalized, "privatekey") ||
			strings.Contains(normalized, "accesskey") || strings.Contains(normalized, "apikey") ||
			strings.HasSuffix(normalized, "signingkey") || strings.HasSuffix(normalized, "encryptionkey")
	}
}

func secretValue(value any) bool {
	switch value := value.(type) {
	case string:
		return secretTextPattern.MatchString(value) || jwtPattern.MatchString(value) || credentialPattern.MatchString(value)
	case map[string]any:
		for key, item := range value {
			if secretKey(key) || secretValue(item) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if secretValue(item) {
				return true
			}
		}
	}
	return false
}
