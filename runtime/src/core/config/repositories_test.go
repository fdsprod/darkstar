package config_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"darkstar/src/core/config"
	"darkstar/src/ports/statestore"
)

func TestRepositorySettingsPrecedenceProvenanceAndIsolation(t *testing.T) {
	root := t.TempDir()
	defaults := config.RepositorySettingsLayer{Scope: config.ScopeDefault, Reference: "shipped", Settings: statestore.RepositorySettings{BaseRef: "HEAD", ValidationProfiles: map[string][]string{"test": {"old"}, "lint": {"lint"}}, PathScope: []string{"src"}}}
	project := config.RepositorySettingsLayer{Scope: config.ScopeProject, Reference: "project@1", Settings: statestore.RepositorySettings{BaseRef: "project", ConfigurationRoot: root, WorktreeBase: "trees"}}
	membership := config.RepositorySettingsLayer{Scope: config.ScopeMembership, Reference: "membership@2", Settings: statestore.RepositorySettings{BaseRef: "membership", ValidationProfiles: map[string][]string{"test": {"new"}}, PathScope: []string{}}}
	cli := config.RepositorySettingsLayer{Scope: config.ScopeCLI, Reference: "request", Settings: statestore.RepositorySettings{BaseRef: "explicit"}}
	got, err := config.ResolveRepositorySettings(cli, membership, defaults, project)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.BaseRef != "explicit" || got.Settings.WorktreeBase != filepath.Join(root, "trees") || got.Settings.PathScope == nil || len(got.Settings.PathScope) != 0 {
		t.Fatalf("wrong resolved settings: %#v", got.Settings)
	}
	if !reflect.DeepEqual(got.Settings.ValidationProfiles, map[string][]string{"test": {"new"}, "lint": {"lint"}}) {
		t.Fatalf("profiles must merge by leaf and replace command lists: %#v", got.Settings.ValidationProfiles)
	}
	if got.Sources["/worktreeBase"].ConfigurationRoot != root || got.Sources["/baseRef"].Scope != "cli" || got.Sources["/validationProfiles/test"].Reference != "membership@2" {
		t.Fatalf("missing provenance: %#v", got.Sources)
	}
	same, err := config.ResolveRepositorySettings(defaults, project, membership, cli)
	if err != nil || same.Digest != got.Digest {
		t.Fatalf("call order changed snapshot digest: %v", err)
	}
	membership.Settings.ValidationProfiles["test"][0] = "changed"
	if got.Settings.ValidationProfiles["test"][0] != "new" {
		t.Fatal("caller mutation changed frozen settings")
	}
	project.Reference = "project@2"
	changed, err := config.ResolveRepositorySettings(defaults, project, membership, cli)
	if err != nil || changed.Digest == got.Digest {
		t.Fatal("snapshot must cover source revisions")
	}
}

func TestRepositorySettingsRejectAmbiguousOriginsAndEscapes(t *testing.T) {
	for _, settings := range []statestore.RepositorySettings{
		{WorktreeBase: "relative"},
		{ConfigurationRoot: "relative"},
		{PathScope: []string{"../secret"}},
		{PathScope: []string{filepath.Join(t.TempDir(), "secret")}},
		{PathScope: []string{"..\\secret"}},
		{ValidationProfiles: map[string][]string{"test": nil}},
	} {
		_, err := config.ResolveRepositorySettings(config.RepositorySettingsLayer{Scope: config.ScopeMembership, Reference: "membership", Settings: settings})
		if err == nil {
			t.Errorf("accepted invalid settings: %#v", settings)
		}
	}
	layer := config.RepositorySettingsLayer{Scope: config.ScopeProject, Reference: "project"}
	if _, err := config.ResolveRepositorySettings(layer, layer); err == nil {
		t.Fatal("duplicate scopes must be rejected")
	}
}

func TestRepositorySettingsCompletePrecedence(t *testing.T) {
	scopes := []config.Scope{config.ScopeDefault, config.ScopeSystem, config.ScopeUser, config.ScopeRepository, config.ScopeProject, config.ScopeMembership, config.ScopeRun, config.ScopeCLI}
	layers := []config.RepositorySettingsLayer{}
	for _, scope := range scopes {
		layers = append(layers, config.RepositorySettingsLayer{Scope: scope, Reference: scope.String(), Settings: statestore.RepositorySettings{BaseRef: scope.String()}})
		got, err := config.ResolveRepositorySettings(layers...)
		if err != nil || got.Settings.BaseRef != scope.String() {
			t.Fatalf("scope %s did not win: %#v %v", scope, got, err)
		}
	}
}
