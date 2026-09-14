package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/investigation"
	"darkstar/src/ports/statestore"
)

type investigationCommandCall struct {
	action   string
	id       string
	key      string
	revision uint64
}

type investigationAPIService struct {
	view     investigation.View
	prepared []investigation.PrepareRequest
	calls    []investigationCommandCall
}

func (s *investigationAPIService) Prepare(_ context.Context, input investigation.PrepareRequest, key string) (investigation.View, error) {
	s.prepared = append(s.prepared, input)
	s.calls = append(s.calls, investigationCommandCall{action: "prepare", key: key})
	return s.view, nil
}

func (s *investigationAPIService) Get(_ context.Context, id string) (investigation.View, error) {
	if id != s.view.Collection.CollectionID {
		return investigation.View{}, statestore.ErrNotFound
	}
	return s.view, nil
}

func (s *investigationAPIService) command(action, id string, version uint64, key string) (investigation.View, error) {
	s.calls = append(s.calls, investigationCommandCall{action: action, id: id, key: key, revision: version})
	if version != s.view.Collection.Revision {
		return investigation.View{}, investigation.ErrConflict
	}
	return s.view, nil
}

func (s *investigationAPIService) Start(_ context.Context, id string, version uint64, key string) (investigation.View, error) {
	return s.command("start", id, version, key)
}

func (s *investigationAPIService) Retry(_ context.Context, id string, version uint64, key string) (investigation.View, error) {
	return s.command("retry", id, version, key)
}

func (s *investigationAPIService) Cancel(_ context.Context, id string, version uint64, key string) (investigation.View, error) {
	return s.command("cancel", id, version, key)
}

func investigationAPIFixture(t *testing.T) (Endpoint, *investigationAPIService) {
	t.Helper()
	service := &investigationAPIService{view: investigation.View{SchemaVersion: 1, Collection: statestore.InvestigationCollection{CollectionID: "investigation_" + strings.Repeat("0", 26), Revision: 3, Status: "prepared"}, Units: []statestore.InvestigationUnit{}, Attempts: []statestore.InvestigationAttempt{}}}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = server.SetInvestigations(service); err != nil {
		t.Fatal(err)
	}
	if err = server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeTestServer(t, server)
	})
	endpoint, _ := server.Endpoint()
	return endpoint, service
}

func TestInvestigationAPIForwardsImmutableInputsAndConcurrencyCommands(t *testing.T) {
	endpoint, service := investigationAPIFixture(t)
	source := `{"scopeId":"scope_00000000000000000000000000","task":{"kind":"feature_brief","featureBrief":{"artifactId":"artifact_brief","version":2,"sha256":"` + strings.Repeat("a", 64) + `"}},"concurrency":4}`
	response := workRequest(t, endpoint, http.MethodPost, "/api/v1/investigations", source, "prepare-exact-input")
	if response.StatusCode != http.StatusCreated || response.Header.Get("ETag") != `"3"` || response.Header.Get("Location") != "/api/v1/investigations/"+service.view.Collection.CollectionID {
		t.Fatalf("prepare response = %d %#v", response.StatusCode, response.Header)
	}
	_ = response.Body.Close()
	if len(service.prepared) != 1 || service.prepared[0].Task.FeatureBrief.SHA256 != strings.Repeat("a", 64) || service.prepared[0].Concurrency != 4 || len(service.calls) != 1 {
		t.Fatal("prepare dropped exact input facts or started execution")
	}
	for _, action := range []string{"start", "retry", "cancel"} {
		response = lifecycleRequest(t, endpoint, http.MethodPost, "/api/v1/investigations/"+service.view.Collection.CollectionID+"/"+action, `{}`, action+"-command-key", `"3"`)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", action, response.StatusCode)
		}
		_ = response.Body.Close()
		last := service.calls[len(service.calls)-1]
		if last.action != action || last.revision != 3 || last.id != service.view.Collection.CollectionID || last.key != action+"-command-key" {
			t.Fatalf("command did not preserve CAS/idempotency: %#v", last)
		}
	}
	response = lifecycleRequest(t, endpoint, http.MethodPost, "/api/v1/investigations/"+service.view.Collection.CollectionID+"/start", `{}`, "stale-version-key", `"2"`)
	assertAPIError(t, response, http.StatusConflict, "INVESTIGATION_REVISION_CONFLICT")
	_ = response.Body.Close()
}

func TestInvestigationAPIRejectsClientExecutionAuthorityAndMissingPreconditions(t *testing.T) {
	endpoint, service := investigationAPIFixture(t)
	resource := "/api/v1/investigations/" + service.view.Collection.CollectionID
	for _, candidate := range []struct{ path, body, key, version string }{
		{resource + "/start", `{}`, "", "\"3\""},
		{resource + "/start", `{}`, "missing-version", ""},
		{resource + "/retry", `{"unitIds":["chosen"]}`, "unit-override-key", "\"3\""},
		{resource + "/cancel", `null`, "null-command-key", "\"3\""},
		{"/api/v1/investigations", `{"scopeId":"scope_00000000000000000000000000","task":{"kind":"text","text":"Inspect"},"provider":"unsafe"}`, "provider-override", ""},
	} {
		response := lifecycleRequest(t, endpoint, http.MethodPost, candidate.path, candidate.body, candidate.key, candidate.version)
		if response.StatusCode < 400 || response.StatusCode >= 500 {
			t.Fatalf("invalid command reached service: %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}
	for _, suffix := range []string{"/results", "/units/submit", "/provider/start", "/attempts"} {
		response := workRequest(t, endpoint, http.MethodPost, resource+suffix, `{}`, "authority-injection")
		assertAPIError(t, response, http.StatusNotFound, "NOT_FOUND")
		_ = response.Body.Close()
	}
	if len(service.calls) != 0 {
		encoded, _ := json.Marshal(service.calls)
		t.Fatalf("rejected requests invoked commands: %s", encoded)
	}
}
