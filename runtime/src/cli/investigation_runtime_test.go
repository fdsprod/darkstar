package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"darkstar/src/core/artifactops"
	"darkstar/src/core/investigation"
	"darkstar/src/core/repositoryscope"
	"darkstar/src/ports/artifactregistry"
	platformport "darkstar/src/ports/platform"
)

func TestNoCodeInvestigationPersistsArtifactAcrossDaemonRestart(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	originalResolver := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) {
		return paths, nil
	}
	t.Cleanup(func() {
		resolveApplicationPaths = originalResolver
	})
	service := startAcceptanceService(t, paths, "45454545454545454545454545454545")
	t.Cleanup(func() {
		_ = service.Close()
	})
	project := createPlanningCLIProject(t, "No code investigation", "investigation-project")
	selectionFile := filepath.Join(root, "repositories.json")
	if err := os.WriteFile(selectionFile, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	var scope repositoryscope.View
	runCLIJSON(t, []string{"project", "scope", "prepare", project.ProjectID, "--repositories-file", selectionFile, "--idempotency-key", "no-code-investigation-scope", "--json"}, &struct {
		Result *repositoryscope.View `json:"result"`
	}{&scope})
	requestFile := filepath.Join(root, "investigation.json")
	request, err := json.Marshal(investigation.PrepareRequest{ScopeID: scope.Scope.ScopeID, Task: investigation.TaskInput{Kind: "text", Text: "Investigate the proposed policy change."}, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestFile, request, 0600); err != nil {
		t.Fatal(err)
	}
	var prepared investigation.View
	runCLIJSON(t, []string{"investigation", "prepare", requestFile, "--idempotency-key", "prepare-no-code-investigation", "--json"}, &struct {
		Result *investigation.View `json:"result"`
	}{&prepared})
	if prepared.Collection.Provider.Provider != "daemon" || len(prepared.Units) != 1 || prepared.Units[0].Kind != "synthesis" {
		t.Fatalf("no-code investigation acquired provider/repository authority: %#v", prepared)
	}
	var started investigation.View
	runCLIJSON(t, []string{"investigation", "start", prepared.Collection.CollectionID, "--revision", strconv.FormatUint(prepared.Collection.Revision, 10), "--idempotency-key", "start-no-code-investigation", "--json"}, &struct {
		Result *investigation.View `json:"result"`
	}{&started})
	var completed investigation.View
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		completed, err = service.investigations.Get(t.Context(), prepared.Collection.CollectionID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Collection.Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(completed.Units) != 1 || completed.Units[0].Status != "succeeded" || completed.Units[0].Result == nil {
		t.Fatalf("no-code investigation did not persist a result: %#v", completed)
	}
	result := completed.Units[0].Result
	var output struct {
		EvidenceStatus string `json:"evidenceStatus"`
	}
	if json.Unmarshal(result.Findings, &output) != nil || output.EvidenceStatus != "no_repository_evidence" {
		t.Fatalf("missing repository evidence was not explicit: %s", result.Findings)
	}
	artifact, err := service.database.ArtifactVersion(t.Context(), result.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := artifact.Provenance.(artifactregistry.InvestigationProvenance); !ok || artifact.BlobDigest != result.Digest {
		t.Fatalf("investigation origin/digest was lost: %#v", artifact)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service = startAcceptanceService(t, paths, "56565656565656565656565656565656")
	var restarted investigation.View
	runCLIJSON(t, []string{"investigation", "show", prepared.Collection.CollectionID, "--json"}, &struct {
		Result *investigation.View `json:"result"`
	}{&restarted})
	if len(restarted.Attempts) != len(completed.Attempts) || restarted.Units[0].Result == nil || restarted.Units[0].Result.Artifact != result.Artifact {
		t.Fatal("restart reran successful no-code work or replaced its artifact")
	}
	var readArtifact artifactops.ArtifactView
	runCLIJSON(t, []string{"artifact", "show-v2", result.Artifact.ArtifactID + "@" + strconv.FormatUint(result.Artifact.Version, 10), "--json"}, &struct {
		Result *artifactops.ArtifactView `json:"result"`
	}{&readArtifact})
	if _, ok := readArtifact.Artifact.Provenance.(artifactregistry.InvestigationProvenance); !ok || readArtifact.Artifact.BlobDigest != result.Digest {
		t.Fatal("versioned artifact read lost immutable investigation provenance")
	}
}
