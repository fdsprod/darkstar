package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"darkstar/src/ports/statestore"
)

// RepositorySettingsLayer carries values with their durable configuration origin.
// Its closed settings type cannot overwrite project identity or tracker bindings.
type RepositorySettingsLayer struct {
	Scope     Scope
	Reference string
	Settings  statestore.RepositorySettings
}

type RepositorySettingSource struct {
	Scope             string `json:"scope"`
	Reference         string `json:"reference"`
	ConfigurationRoot string `json:"configurationRoot,omitempty"`
}

// ResolvedRepositorySettings is an immutable-by-convention snapshot for a run.
// Digest covers both resolved values and the winning provenance of every leaf.
type ResolvedRepositorySettings struct {
	Settings statestore.RepositorySettings      `json:"settings"`
	Sources  map[string]RepositorySettingSource `json:"sources"`
	Digest   string                             `json:"digest"`
}

// ResolveRepositorySettings resolves map leaves independently and replaces lists
// as units. Policy ceilings are intentionally not values in RepositorySettings:
// callers must intersect their independently enforced permission policies.
func ResolveRepositorySettings(input ...RepositorySettingsLayer) (ResolvedRepositorySettings, error) {
	layers := append([]RepositorySettingsLayer(nil), input...)
	sort.Slice(layers, func(i, j int) bool {
		return layers[i].Scope < layers[j].Scope
	})
	result := ResolvedRepositorySettings{Sources: make(map[string]RepositorySettingSource)}
	seen := make(map[Scope]bool)
	for _, layer := range layers {
		if !layer.Scope.valid() || seen[layer.Scope] || strings.TrimSpace(layer.Reference) == "" {
			return result, fmt.Errorf("invalid or duplicate repository configuration scope %s", layer.Scope)
		}
		seen[layer.Scope] = true
		settings := layer.Settings
		if settings.ConfigurationRoot != "" && !filepath.IsAbs(settings.ConfigurationRoot) {
			return result, fmt.Errorf("%s configuration root must be absolute", layer.Scope)
		}
		source := RepositorySettingSource{Scope: layer.Scope.String(), Reference: layer.Reference, ConfigurationRoot: settings.ConfigurationRoot}
		assign := func(path string, value string, target *string) {
			if value != "" {
				*target = value
				result.Sources[path] = source
			}
		}
		assign("/baseRef", settings.BaseRef, &result.Settings.BaseRef)
		assign("/delivery/remote", settings.Delivery.Remote, &result.Settings.Delivery.Remote)
		assign("/delivery/targetBranch", settings.Delivery.TargetBranch, &result.Settings.Delivery.TargetBranch)
		if settings.WorktreeBase != "" {
			base := settings.WorktreeBase
			if !filepath.IsAbs(base) {
				if settings.ConfigurationRoot == "" {
					return result, fmt.Errorf("%s relative worktree base requires its recorded configuration root", layer.Scope)
				}
				base = filepath.Join(settings.ConfigurationRoot, base)
			}
			assign("/worktreeBase", filepath.Clean(base), &result.Settings.WorktreeBase)
		}
		if settings.PathScope != nil {
			for _, path := range settings.PathScope {
				clean := filepath.Clean(filepath.FromSlash(path))
				if strings.TrimSpace(path) == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.Contains(path, "\\") || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
					return result, fmt.Errorf("repository path scope %q must stay inside its repository", path)
				}
			}
			result.Settings.PathScope = append([]string{}, settings.PathScope...)
			result.Sources["/pathScope"] = source
		}
		if settings.ValidationProfiles != nil {
			if result.Settings.ValidationProfiles == nil {
				result.Settings.ValidationProfiles = make(map[string][]string)
			}
			for name, commands := range settings.ValidationProfiles {
				if strings.TrimSpace(name) == "" || commands == nil {
					return result, fmt.Errorf("validation profile requires a name and an explicit command list")
				}
				result.Settings.ValidationProfiles[name] = append([]string{}, commands...)
				result.Sources[jsonPointer([]string{"validationProfiles", name})] = source
			}
		}
	}
	raw, err := json.Marshal(struct {
		Settings statestore.RepositorySettings      `json:"settings"`
		Sources  map[string]RepositorySettingSource `json:"sources"`
	}{result.Settings, result.Sources})
	if err != nil {
		return result, err
	}
	result.Digest = fmt.Sprintf("%x", sha256.Sum256(raw))
	return result, nil
}
