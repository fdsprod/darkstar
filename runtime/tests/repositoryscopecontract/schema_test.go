package repositoryscopecontract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/config"
	"darkstar/src/core/repositoryscope"
	"darkstar/src/ports/repositorysnapshot"
	"darkstar/src/ports/statestore"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func investigationSchema(t *testing.T, definition string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"investigation-scope-v1.schema.json", "repository-membership-v2.schema.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if err = compiler.AddResource("https://darkstar.local/schemas/"+name, document); err != nil {
			t.Fatal(err)
		}
	}
	compiled, err := compiler.Compile("https://darkstar.local/schemas/investigation-scope-v1.schema.json#/$defs/" + definition)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func schemaValue(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func scopeFixture(t *testing.T) repositoryscope.View {
	t.Helper()
	stamp := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	resolved, err := config.ResolveRepositorySettings(config.RepositorySettingsLayer{Scope: config.ScopeProject, Reference: "project@1", Settings: statestore.RepositorySettings{BaseRef: "main", PathScope: []string{"src"}}})
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	projectID := "project_" + strings.Repeat("0", 26)
	scopeID := "scope_" + strings.Repeat("0", 26)
	repositoryID := "repository_" + strings.Repeat("0", 26)
	entry := statestore.FrozenRepositoryScopeEntry{
		Repository: statestore.RepositoryRecord{RepositoryID: repositoryID, Root: "/source", CommonGitDir: "/source/.git", IdentityKey: strings.Repeat("a", 64), CreatedAt: stamp},
		Membership: statestore.RepositoryMembership{ProjectID: projectID, RepositoryID: repositoryID, Revision: 1, Label: "Source", Role: statestore.RepositoryReadOnly, Status: statestore.MembershipActive, UpdatedAt: stamp},
		Ref:        "main", Revision: repositorysnapshot.ResolvedRevision{CommitSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("b", 40)},
		Configuration: statestore.JSONSnapshot(configuration), ConfigurationDigest: resolved.Digest,
	}
	return repositoryscope.View{
		SchemaVersion: 1,
		Scope:         statestore.RepositoryScope{SchemaVersion: 1, ScopeID: scopeID, ProjectID: projectID, ProjectRevision: 1, Mode: statestore.InvestigationScopeReadOnly, Repositories: []statestore.FrozenRepositoryScopeEntry{entry}, ContentPolicy: "committed_only", RequestDigest: strings.Repeat("a", 64), Digest: strings.Repeat("b", 64), CreatedAt: stamp},
		Preparation:   statestore.RepositoryScopePreparation{Status: statestore.RepositoryScopeReady, Revision: 1, UpdatedAt: stamp},
		Evidence:      []statestore.RepositoryScopeEvidence{{ScopeID: scopeID, RepositoryID: repositoryID, Evidence: repositorysnapshot.Evidence{CacheKey: strings.Repeat("a", 64), Root: "/snapshots/immutable", ManifestDigest: strings.Repeat("c", 64), FileCount: 1, TotalBytes: 2, Exclusions: []repositorysnapshot.Exclusion{{Path: "linked", Kind: "symlink", Reason: "Target not followed"}}}}},
	}
}

func TestInvestigationScopeSchemaMatchesTypedServiceRepresentations(t *testing.T) {
	compiled := investigationSchema(t, "InvestigationScopeView")
	for _, state := range []statestore.RepositoryScopePreparationStatus{statestore.RepositoryScopePreparing, statestore.RepositoryScopeReady, statestore.RepositoryScopeBlocked} {
		value := scopeFixture(t)
		value.Preparation.Status = state
		if state == statestore.RepositoryScopeBlocked {
			value.Preparation.Reason = "Snapshot missing; retry preparation with the same key"
		}
		if err := compiled.Validate(schemaValue(t, value)); err != nil {
			t.Fatalf("%s view: %v", state, err)
		}
	}
	value := scopeFixture(t)
	value.Scope.Mode = statestore.InvestigationScopeNone
	value.Scope.Repositories = []statestore.FrozenRepositoryScopeEntry{}
	value.Evidence = []statestore.RepositoryScopeEvidence{}
	if err := compiled.Validate(schemaValue(t, value)); err != nil {
		t.Fatalf("no-repository view: %v", err)
	}
}

func TestInvestigationScopeSchemaRejectsContradictoryScopeAndPreparation(t *testing.T) {
	compiled := investigationSchema(t, "InvestigationScopeView")
	for _, mutate := range []func(*repositoryscope.View){
		func(v *repositoryscope.View) {
			v.Scope.Mode = statestore.InvestigationScopeNone
		},
		func(v *repositoryscope.View) {
			v.Scope.Repositories = []statestore.FrozenRepositoryScopeEntry{}
		},
		func(v *repositoryscope.View) {
			v.Scope.Mode = "write"
		},
		func(v *repositoryscope.View) {
			v.Preparation.Status = statestore.RepositoryScopeBlocked
		},
		func(v *repositoryscope.View) {
			v.Preparation.Reason = "Ready cannot carry a failure reason"
		},
		func(v *repositoryscope.View) {
			v.Scope.Repositories[0].Configuration = statestore.JSONSnapshot(`{"arbitrary":"configuration"}`)
		},
	} {
		value := scopeFixture(t)
		mutate(&value)
		if err := compiled.Validate(schemaValue(t, value)); err == nil {
			t.Fatal("contradictory representation accepted")
		}
	}
}

func TestPrepareScopeSchemaRequiresExplicitBoundedSelection(t *testing.T) {
	compiled := investigationSchema(t, "PrepareInvestigationScopeRequest")
	request := repositoryscope.PrepareRequest{ProjectID: "project_" + strings.Repeat("0", 26), Repositories: []repositoryscope.RepositorySelection{}}
	if err := compiled.Validate(schemaValue(t, request)); err != nil {
		t.Fatal(err)
	}
	request.Repositories = []repositoryscope.RepositorySelection{{RepositoryID: "repository", Ref: "main"}}
	if err := compiled.Validate(schemaValue(t, request)); err != nil {
		t.Fatal(err)
	}
	for _, selections := range [][]repositoryscope.RepositorySelection{nil, {{RepositoryID: "repository", Ref: ""}}, {{RepositoryID: "repository", Ref: " main "}}, make([]repositoryscope.RepositorySelection, 33)} {
		request.Repositories = selections
		if err := compiled.Validate(schemaValue(t, request)); err == nil {
			t.Fatal("implicit or unbounded selection accepted")
		}
	}
}
