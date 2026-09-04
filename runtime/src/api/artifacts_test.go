package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/lateevidence"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactlineage"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/impactassessment"
	"darkstar/src/ports/representationregistry"
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

func TestArtifactWireResponsePreservesExactNestedRepresentationAndProvenance(t *testing.T) {
	when := time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	artifact := artifactregistry.ArtifactVersion{
		ArtifactID: "artifact_one", Version: 2, SourceKind: artifactregistry.SourcePaste, SourceName: "note.json",
		BlobDigest: strings.Repeat("a", 64), Size: 2, DeclaredMediaType: "application/json", DetectedMediaType: "application/json", Locator: "sha256/aa",
		Sensitivity: artifactregistry.SensitivityInternal, Trust: "untrusted", Creator: "user:local", Status: artifactregistry.StatusStored,
		Producer: artifactregistry.Producer{Name: "darkstar-ingest", Version: "1"}, Roles: []string{"note"}, Tags: []string{}, Metadata: map[string]string{},
		Provenance: artifactregistry.AttemptProvenance{RunID: "run_one", NodeID: "design", AttemptID: "attempt_one", OperationID: "operation_one"}, CreatedAt: when,
	}
	representation := representationregistry.Representation{
		RepresentationID: "representation_one", Artifact: artifactregistry.VersionRef{ArtifactID: artifact.ArtifactID, Version: artifact.Version},
		Kind: contentprocessor.RepresentationStructured, Processor: contentprocessor.Descriptor{Name: "common", Version: "1", MediaTypes: []string{"application/json"}},
		MediaType: "application/json", Locator: "sha256/bb", Digest: strings.Repeat("b", 64), Size: 2, TokenEstimate: 1,
		Disclosure: representationregistry.DisclosureRaw, Diagnostics: []string{}, Metadata: map[string]string{}, CreatedAt: when,
	}
	service := &stubArtifactService{showValue: artifactops.ArtifactView{Artifact: artifact, Freshness: artifactlineage.FreshnessCurrent, Representations: []representationregistry.Representation{representation}}}
	server, endpoint := startArtifactTestServer(t, service)
	defer closeTestServer(t, server)

	response := get(t, endpoint.BaseURL()+"/api/v1/artifacts/artifact_one?version=2", endpoint.AuthorizationHeader())
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var decoded artifactops.ArtifactView
	decodeJSON(t, response, &decoded)
	provenance, ok := decoded.Artifact.Provenance.(artifactregistry.AttemptProvenance)
	if !ok || provenance.AttemptID != "attempt_one" {
		t.Fatalf("provenance = %#v", decoded.Artifact.Provenance)
	}
	if len(decoded.Representations) != 1 || decoded.Representations[0].Artifact.Version != 2 || decoded.Representations[0].Processor.Name != "common" {
		t.Fatalf("representations = %#v", decoded.Representations)
	}
}

func TestArtifactContentRoutesUseSafeAuthenticatedHeaders(t *testing.T) {
	digest := strings.Repeat("a", 64)
	service := &stubArtifactService{
		originalBytes:       []byte("<script>unsafe original</script>"),
		originalMeta:        artifactops.Content{Digest: digest, Size: 32, MediaType: "text/html", FileName: "report\"\r\nX-Evil: yes.html"},
		representationBytes: []byte("safe preview"),
		representationMeta:  artifactops.Content{Digest: strings.Repeat("b", 64), Size: 12, MediaType: "text/plain; charset=utf-8", FileName: "representation_preview", RepresentationID: "representation_preview"},
	}
	server, endpoint := startArtifactTestServer(t, service)
	defer closeTestServer(t, server)

	unauthorized, err := http.Get(endpoint.BaseURL() + "/api/v1/artifacts/artifact_one/content?version=1")
	if err != nil {
		t.Fatal(err)
	}
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	_ = unauthorized.Body.Close()

	original := get(t, endpoint.BaseURL()+"/api/v1/artifacts/artifact_one/content?version=1", endpoint.AuthorizationHeader())
	defer func() { _ = original.Body.Close() }()
	content, err := io.ReadAll(original.Body)
	if err != nil {
		t.Fatal(err)
	}
	if original.StatusCode != http.StatusOK || string(content) != string(service.originalBytes) {
		t.Fatalf("original response = %d %q", original.StatusCode, content)
	}
	if got := original.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("original Content-Type = %q", got)
	}
	if disposition := original.Header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") || strings.ContainsAny(disposition, "\r\n") || original.Header.Get("X-Evil") != "" {
		t.Fatalf("unsafe original disposition = %q", disposition)
	}
	if original.Header.Get("ETag") != `"`+digest+`"` || original.Header.Get("X-Darkstar-Content-Digest") != "sha256="+digest {
		t.Fatalf("original digest headers = %q / %q", original.Header.Get("ETag"), original.Header.Get("X-Darkstar-Content-Digest"))
	}
	if original.Header.Get("Cache-Control") != "no-store" || original.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("safe response headers missing: %#v", original.Header)
	}

	preview := get(t, endpoint.BaseURL()+"/api/v1/representations/representation_preview/content", endpoint.AuthorizationHeader())
	defer func() { _ = preview.Body.Close() }()
	previewContent, err := io.ReadAll(preview.Body)
	if err != nil {
		t.Fatal(err)
	}
	if preview.StatusCode != http.StatusOK || preview.Header.Get("Content-Type") != "text/plain" || !strings.HasPrefix(preview.Header.Get("Content-Disposition"), "inline;") || string(previewContent) != "safe preview" {
		t.Fatalf("preview response = %d %q %#v", preview.StatusCode, previewContent, preview.Header)
	}

	service.representationBytes = []byte("<script>unsafe preview</script>")
	service.representationMeta.Size = int64(len(service.representationBytes))
	service.representationMeta.MediaType = "text/html"
	unsafePreview := get(t, endpoint.BaseURL()+"/api/v1/representations/representation_preview/content", endpoint.AuthorizationHeader())
	defer func() { _ = unsafePreview.Body.Close() }()
	if unsafePreview.StatusCode != http.StatusOK || unsafePreview.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(unsafePreview.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("unsafe preview headers = %d %#v", unsafePreview.StatusCode, unsafePreview.Header)
	}

	headRequest, err := http.NewRequestWithContext(context.Background(), http.MethodHead, endpoint.BaseURL()+"/api/v1/artifacts/artifact_one/content?version=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	headRequest.Header.Set("Authorization", endpoint.AuthorizationHeader())
	headResponse, err := (&http.Client{Timeout: 5 * time.Second}).Do(headRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = headResponse.Body.Close() }()
	headBody, err := io.ReadAll(headResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if headResponse.StatusCode != http.StatusOK || len(headBody) != 0 || headResponse.Header.Get("Content-Length") != "32" || headResponse.Header.Get("ETag") != `"`+digest+`"` {
		t.Fatalf("HEAD response = %d %q %#v", headResponse.StatusCode, headBody, headResponse.Header)
	}
}

func TestWithheldRepresentationContentFailsClosed(t *testing.T) {
	service := &stubArtifactService{representationErr: artifactops.ErrContentWithheld}
	server, endpoint := startArtifactTestServer(t, service)
	defer closeTestServer(t, server)

	response := get(t, endpoint.BaseURL()+"/api/v1/representations/representation_secret/content", endpoint.AuthorizationHeader())
	defer func() { _ = response.Body.Close() }()
	assertAPIError(t, response, http.StatusForbidden, "ARTIFACT_CONTENT_WITHHELD")
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
	ingestInput         artifactops.IngestInput
	ingestKey           string
	listInput           artifactops.ListInput
	originalBytes       []byte
	originalMeta        artifactops.Content
	originalErr         error
	representationBytes []byte
	representationMeta  artifactops.Content
	representationErr   error
	showValue           artifactops.ArtifactView
}

func (service *stubArtifactService) Ingest(_ context.Context, input artifactops.IngestInput, key string) (artifactingest.Result, error) {
	service.ingestInput, service.ingestKey = input, key
	return artifactingest.Result{Artifact: artifactregistry.ArtifactVersion{ArtifactID: "artifact_one", Version: 1}}, nil
}

func (*stubArtifactService) Revise(context.Context, string, uint64, artifactops.IngestInput, string) (artifactingest.Result, error) {
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

func (service *stubArtifactService) Show(context.Context, string, uint64) (artifactops.ArtifactView, error) {
	if service.showValue.Artifact.ArtifactID != "" {
		return service.showValue, nil
	}
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

func (service *stubArtifactService) OriginalContent(context.Context, artifactregistry.VersionRef) (artifactops.Content, error) {
	if service.originalErr != nil {
		return artifactops.Content{}, service.originalErr
	}
	value := service.originalMeta
	value.Reader = io.NopCloser(bytes.NewReader(service.originalBytes))
	return value, nil
}

func (service *stubArtifactService) RepresentationContent(context.Context, string) (artifactops.Content, error) {
	if service.representationErr != nil {
		return artifactops.Content{}, service.representationErr
	}
	value := service.representationMeta
	value.Reader = io.NopCloser(bytes.NewReader(service.representationBytes))
	return value, nil
}

var _ ArtifactService = (*stubArtifactService)(nil)
