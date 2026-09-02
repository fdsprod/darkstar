package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/artifactderive"
	"github.com/fdsprod/darkstar/runtime/src/core/artifactingest"
	"github.com/fdsprod/darkstar/runtime/src/core/artifactops"
	"github.com/fdsprod/darkstar/runtime/src/core/lateevidence"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactbinding"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/impactassessment"
	"github.com/fdsprod/darkstar/runtime/src/ports/representationregistry"
)

func TestArtifactIngestRouteRequiresIdempotencyAndReturnsLocation(t *testing.T) {
	service := &stubArtifactService{}
	server, endpoint := startArtifactTestServer(t, service)
	defer closeTestServer(t, server)

	body := []byte(`{"sourceKind":"paste","sourceName":"note.txt","mediaType":"text/plain","content":"aGVsbG8="}`)
	missingKey, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.BaseURL()+"/api/v1/artifacts", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	missingKey.Header.Set("Authorization", endpoint.AuthorizationHeader())
	missingKey.Header.Set("Content-Type", "application/json")
	missingResponse, err := http.DefaultClient.Do(missingKey)
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, missingResponse, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = missingResponse.Body.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.BaseURL()+"/api/v1/artifacts", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "ingest-one")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if location := response.Header.Get("Location"); location != "/api/v1/artifacts/artifact_one?version=1" {
		t.Fatalf("Location = %q", location)
	}
	if service.ingestKey != "ingest-one" || string(service.ingestInput.Content) != "hello" {
		t.Fatalf("ingest request = %#v, key %q", service.ingestInput, service.ingestKey)
	}
}

func TestArtifactListRoutePassesExactTargetFilter(t *testing.T) {
	service := &stubArtifactService{}
	server, endpoint := startArtifactTestServer(t, service)
	defer closeTestServer(t, server)

	response := get(t, endpoint.BaseURL()+"/api/v1/artifacts?targetKind=run&targetId=run_one", endpoint.AuthorizationHeader())
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var result []artifactops.ArtifactView
	decodeJSON(t, response, &result)
	if service.listInput.Target == nil || service.listInput.Target.Kind != artifactbinding.TargetRun || service.listInput.Target.ID != "run_one" {
		t.Fatalf("list input = %#v", service.listInput)
	}
}

func startArtifactTestServer(t *testing.T, service ArtifactService) (*Server, Endpoint) {
	t.Helper()
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetArtifacts(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	endpoint, err := ReadEndpoint(filepath.Dir(server.endpointPath))
	if err != nil {
		t.Fatal(err)
	}
	return server, endpoint
}

type stubArtifactService struct {
	ingestInput artifactops.IngestInput
	ingestKey   string
	listInput   artifactops.ListInput
}

func (service *stubArtifactService) Ingest(_ context.Context, input artifactops.IngestInput, key string) (artifactingest.Result, error) {
	service.ingestInput, service.ingestKey = input, key
	return artifactingest.Result{Artifact: artifactregistry.ArtifactVersion{ArtifactID: "artifact_one", Version: 1}}, nil
}

func (*stubArtifactService) Revise(context.Context, string, artifactops.IngestInput, string) (artifactingest.Result, error) {
	return artifactingest.Result{}, errors.New("not implemented")
}

func (*stubArtifactService) Attach(context.Context, artifactops.AttachInput, string) (artifactbinding.Version, error) {
	return artifactbinding.Version{}, errors.New("not implemented")
}

func (*stubArtifactService) Detach(context.Context, string, string) (artifactbinding.Version, error) {
	return artifactbinding.Version{}, errors.New("not implemented")
}

func (service *stubArtifactService) List(_ context.Context, input artifactops.ListInput) ([]artifactops.ArtifactView, error) {
	service.listInput = input
	return []artifactops.ArtifactView{}, nil
}

func (*stubArtifactService) Show(context.Context, string, uint64) (artifactops.ArtifactView, error) {
	return artifactops.ArtifactView{}, errors.New("not implemented")
}

func (*stubArtifactService) Representations(context.Context, artifactregistry.VersionRef) ([]representationregistry.Representation, error) {
	return nil, errors.New("not implemented")
}

func (*stubArtifactService) Extract(context.Context, artifactregistry.VersionRef, string) (artifactderive.Result, error) {
	return artifactderive.Result{}, errors.New("not implemented")
}

func (*stubArtifactService) Diff(context.Context, string, uint64, uint64) (artifactops.VersionDiff, error) {
	return artifactops.VersionDiff{}, errors.New("not implemented")
}

func (*stubArtifactService) Lint(context.Context, artifactregistry.VersionRef) (artifactops.LintResult, error) {
	return artifactops.LintResult{}, errors.New("not implemented")
}

func (*stubArtifactService) Impact(context.Context, lateevidence.Request) (impactassessment.Assessment, error) {
	return impactassessment.Assessment{}, errors.New("not implemented")
}

var _ ArtifactService = (*stubArtifactService)(nil)
