package configmutation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	configfs "darkstar/src/adapters/configurationstore/filesystem"
	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
	"darkstar/src/core/workmanagement"
	configfiles "darkstar/src/daemon/configuration"
	"darkstar/src/ports/configurationstore"
)

func TestIndependentProjectConfigurationPreservesLegacyFilesAndRecovery(t *testing.T) {
	ctx := context.Background()
	_, database, root := newService(t)
	locations := configfiles.FileLocations{UserConfig: filepath.Join(root, "config", "config.yaml"), UserSecrets: filepath.Join(root, "config", "secrets.yaml"), ProjectConfig: filepath.Join(root, ".darkstar", "config.yaml")}
	files, err := configfs.New(locations, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := configmutation.NewWithProjectStores(files, database, root, files)
	if err != nil {
		t.Fatal(err)
	}
	work, err := workmanagement.New(database)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Legacy", Source: root}, "legacy-config-project")
	if err != nil {
		t.Fatal(err)
	}
	legacyScope, _ := config.ProjectMutationScope(legacy.ProjectID)
	applyBaseRef(t, service, legacyScope, "legacy/main", "legacy-config-base")
	legacyBytes, err := os.ReadFile(locations.ProjectConfig)
	if err != nil {
		t.Fatal(err)
	}
	projects := make([]string, 2)
	for index, key := range []string{"first-config-project", "second-config-project"} {
		project, err := work.CreateProjectV2(ctx, workmanagement.CreateProjectRequest{Name: key}, key)
		if err != nil {
			t.Fatal(err)
		}
		projects[index] = project.Project.ProjectID
	}
	firstScope, _ := config.ProjectMutationScope(projects[0])
	secondScope, _ := config.ProjectMutationScope(projects[1])
	initial, err := service.State(ctx, firstScope)
	if err != nil || effectiveConfigValue(t, initial, "workspace.baseRef") != "origin/HEAD" {
		t.Fatalf("new project inherited daemon project configuration: %#v %v", initial, err)
	}
	preview, err := service.Preview(ctx, configmutation.MutationRequest{Scope: firstScope, Key: "workspace.baseRef", Change: configmutation.Set(config.StringValue("first/main")), ExpectedRevision: initial.Revision})
	if err != nil || !preview.Valid || effectiveConfigValue(t, preview.After, "workspace.baseRef") != "first/main" {
		t.Fatalf("project preview = %#v %v", preview, err)
	}
	unchanged, err := service.State(ctx, firstScope)
	if err != nil || unchanged.Revision != initial.Revision {
		t.Fatal("preview wrote the project configuration")
	}
	first := applyBaseRef(t, service, firstScope, "first/main", "first-config-main")
	second := applyBaseRef(t, service, secondScope, "second/main", "second-config-main")
	for index, state := range []configmutation.State{first, second} {
		for _, setting := range state.Effective {
			if setting.Key == "workspace.baseRef" && (setting.Source.Scope() != config.ScopeProject || !strings.Contains(setting.Source.Reference(), projects[index])) {
				t.Fatalf("configuration lost project-specific provenance: %#v", setting)
			}
		}
	}
	first = applyBaseRef(t, service, firstScope, "first/next", "first-config-next")
	second = applyBaseRef(t, service, secondScope, "second/next", "second-config-next")
	restored, err := service.Restore(ctx, configmutation.RestoreRequest{Scope: firstScope, ExpectedRevision: first.Revision, IdempotencyKey: "first-config-restore"})
	if err != nil || effectiveConfigValue(t, restored.State, "workspace.baseRef") != "first/main" {
		t.Fatalf("project recovery selected another project's backup: %#v %v", restored, err)
	}
	secondAfter, err := service.State(ctx, secondScope)
	if err != nil || secondAfter.Revision != second.Revision || effectiveConfigValue(t, secondAfter, "workspace.baseRef") != "second/next" {
		t.Fatal("restoring first project changed second project")
	}
	currentLegacy, err := os.ReadFile(locations.ProjectConfig)
	if err != nil || string(currentLegacy) != string(legacyBytes) {
		t.Fatal("v2 operations changed legacy daemon project file")
	}
	// Constructing a new service/store preserves identity-selected files and
	// their recovery history without a cache or current UI selection.
	reopenedFiles, err := configfs.New(locations, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := configmutation.NewWithProjectStores(reopenedFiles, database, root, reopenedFiles)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := reopened.State(ctx, firstScope)
	if err != nil || retained.Revision != restored.State.Revision {
		t.Fatalf("restarted configuration lost project state: %#v %v", retained, err)
	}
	legacyOther, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Unresolved", Source: t.TempDir()}, "unresolved-config-project")
	if err != nil {
		t.Fatal(err)
	}
	unresolvedScope, _ := config.ProjectMutationScope(legacyOther.ProjectID)
	if _, err := reopened.State(ctx, unresolvedScope); !errors.Is(err, configmutation.ErrProjectMismatch) {
		t.Fatalf("unresolved legacy project silently adopted a configuration: %v", err)
	}
}

func TestProjectStoreConcurrentCompareAndSwapAndPathBoundary(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	files, err := configfs.New(configfiles.FileLocations{UserConfig: filepath.Join(root, "user.yaml"), UserSecrets: filepath.Join(root, "secrets.yaml"), ProjectConfig: filepath.Join(root, "legacy.yaml")}, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "../other", "project_../../user", `project_\..\outside`} {
		if _, err := files.ForProject(invalid); !errors.Is(err, configurationstore.ErrPathBoundary) {
			t.Fatalf("accepted project configuration path %q: %v", invalid, err)
		}
	}
	projectID := "project_01K3Z1C2AAAAAAAAAAAAAAAAAA"
	first, err := files.ForProject(projectID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := files.ForProject(projectID)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := first.Snapshot(ctx, configurationstore.TargetProject)
	if err != nil {
		t.Fatal(err)
	}
	stores := []configurationstore.Store{first, second}
	results := make([]error, 2)
	var wait sync.WaitGroup
	for index := range stores {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, results[index] = stores[index].Apply(ctx, configurationstore.TargetProject, configurationstore.Mutation{Operation: configurationstore.OperationSet, Path: []string{"workspace", "baseRef"}, Value: []string{"main", "release"}[index]}, initial.Revision)
		}(index)
	}
	wait.Wait()
	successes, conflicts := 0, 0
	for _, err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, configurationstore.ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent project views bypassed compare-and-swap: %v", results)
	}
}

func applyBaseRef(t *testing.T, service *configmutation.Service, scope config.MutationScope, value, key string) configmutation.State {
	t.Helper()
	ctx := context.Background()
	state, err := service.State(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, configmutation.ApplyRequest{MutationRequest: configmutation.MutationRequest{Scope: scope, Key: "workspace.baseRef", Change: configmutation.Set(config.StringValue(value)), ExpectedRevision: state.Revision}, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}

func effectiveConfigValue(t *testing.T, state configmutation.State, key string) any {
	t.Helper()
	for _, setting := range state.Effective {
		if setting.Key == key {
			return setting.Value.Value()
		}
	}
	t.Fatalf("missing effective setting %s", key)
	return nil
}
