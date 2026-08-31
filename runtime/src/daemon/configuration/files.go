// Package configuration composes configuration files with the deterministic
// core resolver.
package configuration

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fdsprod/darkstar/runtime/src/core/config"
	"github.com/fdsprod/darkstar/runtime/src/ports/platform"
	"go.yaml.in/yaml/v3"
)

const (
	// MaxFileSize bounds each human-authored configuration document.
	MaxFileSize = 1 << 20

	userConfigName    = "config.yaml"
	userSecretsName   = "secrets.yaml"
	projectDirectory  = ".darkstar"
	projectConfigName = "config.yaml"
)

// FileLocations keeps project configuration separate from user-only settings
// and secrets. Secrets are deliberately not part of the ordinary layer loader.
type FileLocations struct {
	UserConfig    string
	UserSecrets   string
	ProjectConfig string
}

// ResolveFileLocations derives canonical filenames from platform-owned paths
// and a previously discovered project root.
func ResolveFileLocations(paths platform.Paths, projectRoot string) (FileLocations, error) {
	if !filepath.IsAbs(paths.Config) {
		return FileLocations{}, fmt.Errorf("platform configuration directory must be absolute: %q", paths.Config)
	}
	if !filepath.IsAbs(projectRoot) {
		return FileLocations{}, fmt.Errorf("project root must be absolute: %q", projectRoot)
	}
	return FileLocations{
		UserConfig:    filepath.Join(filepath.Clean(paths.Config), userConfigName),
		UserSecrets:   filepath.Join(filepath.Clean(paths.Config), userSecretsName),
		ProjectConfig: filepath.Join(filepath.Clean(projectRoot), projectDirectory, projectConfigName),
	}, nil
}

// Resolve loads optional user and project files and applies any run/CLI
// overrides. Missing optional files contribute no layer.
func Resolve(defaults config.Layer, locations FileLocations, overrides ...config.Layer) (config.Effective, error) {
	layers := make([]config.Layer, 0, 2+len(overrides))
	for _, candidate := range []struct {
		scope config.Scope
		path  string
	}{
		{scope: config.ScopeUser, path: locations.UserConfig},
		{scope: config.ScopeProject, path: locations.ProjectConfig},
	} {
		layer, found, err := LoadOptionalFile(candidate.scope, candidate.path)
		if err != nil {
			return config.Effective{}, err
		}
		if found {
			layers = append(layers, layer)
		}
	}
	layers = append(layers, overrides...)
	return config.Resolve(defaults, layers...)
}

// LoadOptionalFile reads one user or project YAML layer. A missing file is not
// an error; malformed, oversized, or multi-document YAML fails closed.
func LoadOptionalFile(scope config.Scope, path string) (config.Layer, bool, error) {
	if scope != config.ScopeUser && scope != config.ScopeProject {
		return config.Layer{}, false, fmt.Errorf("file layer scope must be user or project, got %s", scope)
	}
	if !filepath.IsAbs(path) {
		return config.Layer{}, false, fmt.Errorf("configuration path must be absolute: %q", path)
	}

	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return config.Layer{}, false, nil
	}
	if err != nil {
		return config.Layer{}, false, fmt.Errorf("open %s configuration %q: %w", scope, path, err)
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	if err != nil {
		return config.Layer{}, false, fmt.Errorf("read %s configuration %q: %w", scope, path, err)
	}
	if len(content) > MaxFileSize {
		return config.Layer{}, false, fmt.Errorf("%s configuration %q exceeds %d bytes", scope, path, MaxFileSize)
	}

	values, err := decodeSingleDocument(content)
	if err != nil {
		return config.Layer{}, false, fmt.Errorf("decode %s configuration %q: %w", scope, path, err)
	}
	var layer config.Layer
	switch scope {
	case config.ScopeUser:
		layer, err = config.UserFile(path, values)
	case config.ScopeProject:
		layer, err = config.ProjectFile(path, values)
	}
	if err != nil {
		return config.Layer{}, false, err
	}
	return layer, true, nil
}

func decodeSingleDocument(content []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	values := make(map[string]any)
	if err := decoder.Decode(&values); errors.Is(err, io.EOF) {
		return values, nil
	} else if err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not supported")
		}
		return nil, err
	}
	return values, nil
}
