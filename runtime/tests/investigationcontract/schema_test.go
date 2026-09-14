package investigationcontract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/investigation"
	"darkstar/src/core/investigationrunner"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/statestore"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func contract(t *testing.T, file, definition string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"investigation-v1.schema.json", "feature-planning-v1alpha1.schema.json", "artifact-v1alpha3.schema.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://darkstar.local/schemas/"+name, value); err != nil {
			t.Fatal(err)
		}
	}
	compiled, err := compiler.Compile("https://darkstar.local/schemas/" + file + "#/$defs/" + definition)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func encoded(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func findingsFixture() investigationrunner.RepositoryFindings {
	return investigationrunner.RepositoryFindings{SchemaVersion: 1, RepositoryID: "repository_source", CommitSHA: strings.Repeat("a", 40), Quality: "complete", Summary: "The public interface lives in this source file.", CodeReferences: []investigationrunner.CodeReference{{RepositoryID: "repository_source", CommitSHA: strings.Repeat("a", 40), BlobSHA: strings.Repeat("b", 40), Path: "src/api.go", StartLine: 1, EndLine: 3}}, AffectedInterfaces: []string{"Public API"}, ReusablePatterns: []string{}, Constraints: []string{}, Risks: []string{}, UnresolvedQuestions: []string{}, Limitations: []string{}}
}

func TestInvestigationOutputContractsMatchTypedCompletePartialAndMissingEvidence(t *testing.T) {
	findings := contract(t, "investigation-v1.schema.json", "RepositoryFindings")
	for _, quality := range []string{"complete", "partial", "missing"} {
		value := findingsFixture()
		value.Quality = quality
		if quality != "complete" {
			value.Limitations = []string{"Some source was unavailable"}
		}
		if quality == "missing" {
			value.CodeReferences = []investigationrunner.CodeReference{}
		}
		if err := findings.Validate(encoded(t, value)); err != nil {
			t.Fatalf("%s typed findings: %v", quality, err)
		}
	}
	synthesis := contract(t, "investigation-v1.schema.json", "InvestigationSynthesis")
	value := investigationrunner.Synthesis{SchemaVersion: 1, Quality: "partial", EvidenceStatus: "no_repository_evidence", Summary: "The scope supplies no repository evidence.", Findings: []investigationrunner.FindingReference{}, Missing: []investigation.MissingUnit{}, CrossRepositoryImplications: []string{}, Constraints: []string{}, Risks: []string{}, UnresolvedQuestions: []string{}, Limitations: []string{"No code was investigated"}}
	if err := synthesis.Validate(encoded(t, value)); err != nil {
		t.Fatal(err)
	}
	value.EvidenceStatus = "repository_evidence"
	value.Findings = []investigationrunner.FindingReference{{RepositoryID: "repository_source", ArtifactID: "artifact_findings", Version: 1, Digest: strings.Repeat("c", 64)}}
	value.Missing = []investigation.MissingUnit{{RepositoryID: "repository_other", Reason: "Provider unavailable"}}
	if err := synthesis.Validate(encoded(t, value)); err != nil {
		t.Fatal(err)
	}
	value.Quality = "complete"
	if err := synthesis.Validate(encoded(t, value)); err == nil {
		t.Fatal("missing evidence was accepted as complete synthesis")
	}
}

func TestInvestigationOutputContractsRejectContradictoryClaims(t *testing.T) {
	compiled := contract(t, "investigation-v1.schema.json", "RepositoryFindings")
	for _, mutate := range []func(*investigationrunner.RepositoryFindings){
		func(v *investigationrunner.RepositoryFindings) {
			v.CodeReferences = []investigationrunner.CodeReference{}
		},
		func(v *investigationrunner.RepositoryFindings) {
			v.Quality = "partial"
		},
		func(v *investigationrunner.RepositoryFindings) {
			v.Quality = "missing"
			v.Limitations = []string{"Missing"}
		},
		func(v *investigationrunner.RepositoryFindings) {
			v.CodeReferences[0].BlobSHA = "HEAD"
		},
		func(v *investigationrunner.RepositoryFindings) {
			v.CodeReferences[0].StartLine = 0
		},
		func(v *investigationrunner.RepositoryFindings) {
			v.Risks = nil
		},
	} {
		value := findingsFixture()
		mutate(&value)
		if err := compiled.Validate(encoded(t, value)); err == nil {
			t.Fatal("contradictory or unresolved output accepted")
		}
	}
}

func TestInvestigationCollectionAndProvenanceHaveNoInventedWorkflowIdentity(t *testing.T) {
	stamp := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	id := "investigation_" + strings.Repeat("0", 26)
	unitID := "investigation_unit_" + strings.Repeat("0", 26)
	view := investigation.View{SchemaVersion: 1, Collection: statestore.InvestigationCollection{SchemaVersion: 1, CollectionID: id, ProjectID: "project_" + strings.Repeat("0", 26), ScopeID: "scope_" + strings.Repeat("0", 26), ScopeDigest: strings.Repeat("a", 64), Task: statestore.InvestigationFrozenTask{Kind: "text", Content: json.RawMessage(`{"task":"Investigate this idea"}`), Digest: strings.Repeat("b", 64)}, Provider: statestore.InvestigationProviderSelection{Provider: "daemon", CapabilityFingerprint: strings.Repeat("c", 64)}, RepositoryIDs: []string{}, Concurrency: 2, RequestDigest: strings.Repeat("d", 64), CreatedAt: stamp, Status: "prepared", Revision: 1, UpdatedAt: stamp}, Units: []statestore.InvestigationUnit{{UnitID: unitID, CollectionID: id, Kind: "synthesis", Status: "pending"}}, Attempts: []statestore.InvestigationAttempt{}}
	if err := contract(t, "investigation-v1.schema.json", "InvestigationView").Validate(encoded(t, view)); err != nil {
		t.Fatalf("typed no-code collection: %v", err)
	}
	provenance := artifactregistry.InvestigationProvenance{CollectionID: id, UnitID: unitID, AttemptID: "attempt_" + strings.Repeat("0", 26), OperationID: "operation_ingest"}
	compiled := contract(t, "artifact-v1alpha3.schema.json", "ArtifactProvenance")
	value := encoded(t, provenance).(map[string]any)
	if err := compiled.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["runId"] = "run_fake"
	if err := compiled.Validate(value); err == nil {
		t.Fatal("investigation artifact accepted an invented workflow identity")
	}
}
