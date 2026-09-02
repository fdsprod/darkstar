package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"darkstar/src/core/runexport"
	"darkstar/src/ports/statestore"
)

func TestRunExportRequiresAuthenticationAndReturnsZIP(t *testing.T) {
	t.Parallel()
	runID := "run_01K3Z1C2AAAAAAAAAAAAAAAAAA"
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetRunExporter(staticRunExporter{content: []byte("PK-test")}); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, found := server.Endpoint()
	if !found {
		t.Fatal("started server has no endpoint")
	}

	unauthorized := get(t, endpoint.BaseURL()+"/api/v1/runs/"+runID+"/export", "")
	defer func() {
		_ = unauthorized.Body.Close()
	}()
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "UNAUTHENTICATED")

	response := get(t, endpoint.BaseURL()+"/api/v1/runs/"+runID+"/export", endpoint.AuthorizationHeader())
	defer func() {
		_ = response.Body.Close()
	}()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(content) != "PK-test" || response.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("export response status=%d headers=%v body=%q", response.StatusCode, response.Header, content)
	}
	if response.Header.Get("Content-Disposition") != `attachment; filename="`+runID+`.zip"` || response.Header.Get("X-Darkstar-Export-Schema") != "1" {
		t.Fatalf("export headers = %v", response.Header)
	}
}

func TestRunExportMapsMissingAndInvalidRunsToNotFound(t *testing.T) {
	t.Parallel()
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetRunExporter(staticRunExporter{err: statestore.ErrNotFound}); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	for _, resource := range []string{"/api/v1/runs/not-a-run/export", "/api/v1/runs/run_01K3Z1C2AAAAAAAAAAAAAAAAAA/export"} {
		response := get(t, endpoint.BaseURL()+resource, endpoint.AuthorizationHeader())
		assertAPIError(t, response, http.StatusNotFound, "NOT_FOUND")
		_ = response.Body.Close()
	}
	if err := server.SetRunExporter(staticRunExporter{}); err == nil {
		t.Fatal("SetRunExporter succeeded after server start")
	}
}

type staticRunExporter struct {
	content []byte
	err     error
}

func (exporter staticRunExporter) Build(context.Context, string) ([]byte, runexport.Manifest, error) {
	if exporter.err != nil {
		return nil, runexport.Manifest{}, exporter.err
	}
	if exporter.content == nil {
		return nil, runexport.Manifest{}, errors.New("export unavailable")
	}
	return exporter.content, runexport.Manifest{SchemaVersion: 1}, nil
}
