package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	localapi "darkstar/src/api"
	"darkstar/src/daemon"
	"darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

func TestDaemonServiceReconcilesBeforePublishingAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	runtimeDirectory := filepath.Join(root, "runtime")
	dataDirectory := filepath.Join(root, "data")
	paths := platform.Paths{
		Config: filepath.Join(root, "config"), Data: dataDirectory, Cache: filepath.Join(root, "cache"),
		Logs: filepath.Join(root, "logs"), Runtime: runtimeDirectory,
	}
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(ctx, filepath.Join(dataDirectory, "darkstar.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.AcquireLease(ctx, statestore.AcquireLeaseRequest{
		LeaseID: "lease_repo", ScopeKind: statestore.LeaseScopeRepository, ScopeID: "repo_01",
		HolderAttemptID: "attempt_01", DaemonInstanceID: "daemon_old", HostBootID: "boot_01", Duration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	server, err := localapi.NewServer(runtimeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	service := &daemonAPIService{server: server, paths: paths, projectRoot: root}
	state := daemon.State{
		SchemaVersion: 1,
		InstanceID:    "11111111111111111111111111111111",
		Process: daemon.ProcessIdentity{
			PID: os.Getpid(), StartedAt: time.Now().UTC(), Executable: filepath.Join(root, "darkstar.exe"),
		},
	}
	if err := service.Start(ctx, state); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	}()
	endpoint, found := server.Endpoint()
	if !found {
		t.Fatal("API endpoint was not published")
	}
	request, err := http.NewRequest(http.MethodGet, endpoint.BaseURL()+"/api/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	var rootResponse struct {
		Recovery localapi.RecoveryStatus `json:"recovery"`
	}
	if err := json.NewDecoder(response.Body).Decode(&rootResponse); err != nil {
		t.Fatal(err)
	}
	if rootResponse.Recovery.SchedulingAllowed() ||
		rootResponse.Recovery.Reconciled != 1 || rootResponse.Recovery.ReconcileRequired != 1 {
		t.Fatalf("recovery status = %#v", rootResponse.Recovery)
	}
	var leaseState string
	if err := service.database.SQL().QueryRowContext(ctx, `SELECT state FROM leases WHERE lease_id = 'lease_repo'`).Scan(&leaseState); err != nil {
		t.Fatal(err)
	}
	if leaseState != "reconcile_required" {
		t.Fatalf("lease state = %s", leaseState)
	}
}
