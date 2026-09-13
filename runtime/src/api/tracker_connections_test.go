package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/trackerconnection"
)

type connectionAPIFixture struct {
	credentialRef     string
	secret            string
	stored            int
	requestedRevision string
	cursor            string
	pageSize          int
}

func (f *connectionAPIFixture) StoreCredential(_ context.Context, ref, secret string) error {
	f.credentialRef = ref
	f.secret = secret
	f.stored++
	return nil
}

func (f *connectionAPIFixture) record() trackerconnection.Record {
	return trackerconnection.Record{SchemaVersion: 1, ConnectionID: "linear-main", Revision: "r1", Configuration: trackerconnection.LinearConfiguration{CredentialRef: "linear-secret", Authentication: "personal_api_key", AccountID: "account-id", WorkspaceID: "workspace-id"}, AccountName: "Test account", ObservedAt: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), EvidenceRef: "tracker-evidence:connection"}
}

func (f *connectionAPIFixture) CreateLinear(_ context.Context, _ trackerconnection.LinearSetupRequest) (trackerconnection.Record, error) {
	return f.record(), nil
}

func (f *connectionAPIFixture) CreateGitHubToken(_ context.Context, _ trackerconnection.GitHubTokenSetupRequest) (trackerconnection.Record, error) {
	return f.record(), nil
}

func (f *connectionAPIFixture) CreateGitHubCLI(_ context.Context, _ trackerconnection.GitHubCLISetupRequest) (trackerconnection.Record, error) {
	return f.record(), nil
}

func (f *connectionAPIFixture) List(context.Context) ([]trackerconnection.Record, error) {
	return []trackerconnection.Record{f.record()}, nil
}

func (f *connectionAPIFixture) Get(_ context.Context, _, revision string) (trackerconnection.Record, error) {
	f.requestedRevision = revision
	return f.record(), nil
}

func (f *connectionAPIFixture) Health(_ context.Context, _, revision string) (trackerconnection.Health, error) {
	f.requestedRevision = revision
	return trackerconnection.Health{SchemaVersion: 1, Account: trackerconnection.NamedID{ID: "account-id", Name: "Test account"}, ObservedAt: f.record().ObservedAt, EvidenceRef: "health-evidence"}, nil
}

func (f *connectionAPIFixture) Destinations(_ context.Context, _, revision, cursor string, pageSize int) (trackerconnection.Destinations, error) {
	f.requestedRevision = revision
	f.cursor = cursor
	f.pageSize = pageSize
	return trackerconnection.Destinations{SchemaVersion: 1, Destinations: []trackerconnection.Destination{{Provider: "linear", Host: "linear.app", TenantID: "workspace-id", ScopeID: "workspace-id", ContainerID: "team-id", Name: "Team", Projects: []trackerconnection.NamedID{}}}, ObservedAt: f.record().ObservedAt, EvidenceRefs: []string{"discovery-evidence"}}, nil
}

func TestTrackerConnectionAPIProtectsTransientCredentialInputAndExactDiscovery(t *testing.T) {
	service := &connectionAPIFixture{}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetTrackerConnections(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	secret := "synthetic-secret-never-echo"
	response := workRequest(t, endpoint, http.MethodPost, "/api/v1/tracker/credentials/linear-secret", `{"schemaVersion":1,"secret":"`+secret+`"}`, "")
	content, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || service.stored != 1 || service.secret != secret || service.credentialRef != "linear-secret" || strings.Contains(string(content), secret) {
		t.Fatal("credential endpoint failed its transient input contract")
	}
	response = workRequest(t, endpoint, http.MethodPost, "/api/v1/tracker/credentials/linear-secret", `{"schemaVersion":1,"secret":"value","persistPlaintext":true}`, "")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || service.stored != 1 {
		t.Fatal("credential endpoint accepted unknown fields")
	}
	response = workRequest(t, endpoint, http.MethodGet, "/api/v1/tracker/connections", "", "")
	var listed struct {
		SchemaVersion int                        `json:"schemaVersion"`
		Connections   []trackerconnection.Record `json:"connections"`
	}
	decodeJSON(t, response, &listed)
	_ = response.Body.Close()
	if listed.SchemaVersion != 1 || len(listed.Connections) != 1 {
		t.Fatal("connection list omitted versioned records")
	}
	encoded, _ := json.Marshal(listed)
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), `"kind":"linear"`) {
		t.Fatal("connection record lost its variant or exposed credentials")
	}
	path := "/api/v1/tracker/connections/linear-main/revisions/r1/destinations?cursor=next-page&pageSize=7"
	response = workRequest(t, endpoint, http.MethodGet, path, "", "")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || service.requestedRevision != "r1" || service.cursor != "next-page" || service.pageSize != 7 {
		t.Fatal("destination discovery did not preserve exact revision/query")
	}
	response = workRequest(t, endpoint, http.MethodGet, path+"&pageSize=8", "", "")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatal("destination discovery accepted repeated query parameters")
	}
}
