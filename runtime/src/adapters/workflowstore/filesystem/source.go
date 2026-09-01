// Package filesystem discovers authored workflows from configured directories.
package filesystem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports/platform"
	"github.com/fdsprod/darkstar/runtime/src/ports/workflowstore"
	"go.yaml.in/yaml/v3"
)

const MaxDocumentSize = 1 << 20

// Directory binds one absolute directory to its configuration scope. Missing
// optional directories are treated as empty scopes.
type Directory struct {
	Scope workflowstore.Scope
	Path  string
}

// Source reads workflow documents from configured scope directories.
type Source struct {
	directories []Directory
}

var _ workflowstore.Source = (*Source)(nil)

// ResolveDirectories derives the standard shipped, user, and project workflow
// directories from platform-owned paths and the discovered project root.
func ResolveDirectories(defaultDirectory string, paths platform.Paths, projectRoot string) ([]Directory, error) {
	for _, candidate := range []struct {
		label string
		path  string
	}{
		{label: "default workflow directory", path: defaultDirectory},
		{label: "user configuration directory", path: paths.Config},
		{label: "project root", path: projectRoot},
	} {
		if !filepath.IsAbs(candidate.path) {
			return nil, fmt.Errorf("%s must be absolute: %q", candidate.label, candidate.path)
		}
	}
	return []Directory{
		{Scope: workflowstore.ScopeDefault, Path: filepath.Clean(defaultDirectory)},
		{Scope: workflowstore.ScopeUser, Path: filepath.Join(filepath.Clean(paths.Config), "workflows")},
		{Scope: workflowstore.ScopeProject, Path: filepath.Join(filepath.Clean(projectRoot), ".darkstar", "workflows")},
	}, nil
}

// New creates a source with at most one directory per scope.
func New(directories ...Directory) (*Source, error) {
	seen := make(map[workflowstore.Scope]struct{}, len(directories))
	configured := make([]Directory, len(directories))
	for index, directory := range directories {
		if !validScope(directory.Scope) {
			return nil, fmt.Errorf("invalid workflow source scope %q", directory.Scope)
		}
		if _, exists := seen[directory.Scope]; exists {
			return nil, fmt.Errorf("duplicate %s workflow source", directory.Scope)
		}
		if !filepath.IsAbs(directory.Path) {
			return nil, fmt.Errorf("%s workflow directory must be absolute: %q", directory.Scope, directory.Path)
		}
		seen[directory.Scope] = struct{}{}
		configured[index] = Directory{Scope: directory.Scope, Path: filepath.Clean(directory.Path)}
	}
	sort.Slice(configured, func(i, j int) bool { return precedence(configured[i].Scope) < precedence(configured[j].Scope) })
	return &Source{directories: configured}, nil
}

// Load reads direct child JSON/YAML files in stable scope/path order.
func (s *Source) Load(ctx context.Context) ([]workflowstore.Candidate, error) {
	if s == nil {
		return nil, errors.New("workflow filesystem source is nil")
	}
	var candidates []workflowstore.Candidate
	for _, directory := range s.directories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(directory.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s workflow directory %q: %w", directory.Scope, directory.Path, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !workflowExtension(entry.Name()) {
				continue
			}
			path := filepath.Join(directory.Path, entry.Name())
			content, err := readDocument(path)
			if err != nil {
				return nil, fmt.Errorf("load %s workflow %q: %w", directory.Scope, path, err)
			}
			candidates = append(candidates, workflowstore.Candidate{
				Scope: directory.Scope, Reference: path, Content: content,
			})
		}
	}
	return candidates, nil
}

func readDocument(path string) (json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, MaxDocumentSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxDocumentSize {
		return nil, fmt.Errorf("document exceeds %d bytes", MaxDocumentSize)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if len(document.Content) == 0 {
		return nil, errors.New("document is empty")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not supported")
		}
		return nil, err
	}
	if err := validateYAMLNode(document.Content[0]); err != nil {
		return nil, err
	}
	var value any
	if err := document.Decode(&value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("convert document to JSON: %w", err)
	}
	return encoded, nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are not supported")
	}
	if node.Kind == yaml.ScalarNode {
		switch node.Tag {
		case "!!null", "!!bool", "!!int", "!!float", "!!str":
			return nil
		default:
			return fmt.Errorf("non-JSON YAML scalar tag %q is not supported", node.Tag)
		}
	}
	if node.Kind == yaml.MappingNode {
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" {
				return errors.New("workflow object keys must be ordinary strings")
			}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func workflowExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func validScope(scope workflowstore.Scope) bool {
	return scope == workflowstore.ScopeDefault || scope == workflowstore.ScopeUser || scope == workflowstore.ScopeProject
}

func precedence(scope workflowstore.Scope) int {
	switch scope {
	case workflowstore.ScopeDefault:
		return 1
	case workflowstore.ScopeUser:
		return 2
	case workflowstore.ScopeProject:
		return 3
	default:
		return 0
	}
}
