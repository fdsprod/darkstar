package client_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	localapi "github.com/fdsprod/darkstar/runtime/src/api"
	clientapi "github.com/fdsprod/darkstar/runtime/src/api/client"
)

func TestConnectDiscoversRunningDaemonWithoutAutostart(t *testing.T) {
	t.Parallel()

	runtimeDirectory := absoluteTempDirectory(t)
	server := startServer(t, runtimeDirectory)
	var starts atomic.Int32
	client := newClient(t, clientapi.Config{
		RuntimeDirectory: runtimeDirectory,
		Autostart: func(context.Context) error {
			starts.Add(1)
			return nil
		},
	})

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if session.Version() != localapi.VersionV1 {
		t.Fatalf("Version() = %q, want %q", session.Version(), localapi.VersionV1)
	}
	if session.Endpoint().Port != endpointPort(t, server) {
		t.Fatal("session did not retain the discovered endpoint")
	}
	if !session.Recovery().SchedulingAllowed() {
		t.Fatalf("Recovery() = %#v", session.Recovery())
	}
	if starts.Load() != 0 {
		t.Fatalf("autostart calls = %d, want 0", starts.Load())
	}
}

func TestConnectAutostartsOnceWhenEndpointIsMissing(t *testing.T) {
	t.Parallel()

	runtimeDirectory := absoluteTempDirectory(t)
	var starts atomic.Int32
	var server *localapi.Server
	client := newClient(t, clientapi.Config{
		RuntimeDirectory: runtimeDirectory,
		Autostart: func(ctx context.Context) error {
			starts.Add(1)
			var err error
			server, err = localapi.NewServer(runtimeDirectory)
			if err != nil {
				return err
			}
			return server.Start(ctx, os.Getpid(), time.Now().UTC())
		},
	})
	t.Cleanup(func() {
		if server != nil {
			_ = server.Close()
		}
	})

	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("autostart calls = %d, want 1", starts.Load())
	}
}

func TestConnectDoesNotAutostartForIncompatibleVersion(t *testing.T) {
	t.Parallel()

	runtimeDirectory := absoluteTempDirectory(t)
	startServer(t, runtimeDirectory)
	var starts atomic.Int32
	client := newClient(t, clientapi.Config{
		RuntimeDirectory: runtimeDirectory,
		Versions:         []localapi.Version{"v2"},
		Autostart: func(context.Context) error {
			starts.Add(1)
			return nil
		},
	})

	_, err := client.Connect(context.Background())
	var failure *clientapi.Failure
	if !errors.As(err, &failure) || failure.Kind != clientapi.FailureIncompatible {
		t.Fatalf("Connect() error = %T %v, want incompatible Failure", err, err)
	}
	if starts.Load() != 0 {
		t.Fatalf("autostart calls = %d, want 0", starts.Load())
	}
}

func TestDoJSONReturnsStableAPIError(t *testing.T) {
	t.Parallel()

	runtimeDirectory := absoluteTempDirectory(t)
	startServer(t, runtimeDirectory)
	client := newClient(t, clientapi.Config{RuntimeDirectory: runtimeDirectory})
	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	err = session.DoJSON(context.Background(), http.MethodGet, "missing", nil, nil)
	var problem *clientapi.APIError
	if !errors.As(err, &problem) {
		t.Fatalf("DoJSON() error = %T %v, want APIError", err, err)
	}
	if problem.HTTPStatus != http.StatusNotFound || problem.Code != "NOT_FOUND" || problem.SchemaVersion != 1 {
		t.Fatalf("DoJSON() error = %#v", problem)
	}
}

func TestDoJSONRejectsResourceOutsideAPIRoot(t *testing.T) {
	t.Parallel()

	runtimeDirectory := absoluteTempDirectory(t)
	startServer(t, runtimeDirectory)
	client := newClient(t, clientapi.Config{RuntimeDirectory: runtimeDirectory})
	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	err = session.DoJSON(context.Background(), http.MethodGet, "../health", nil, nil)
	var failure *clientapi.Failure
	if !errors.As(err, &failure) || failure.Kind != clientapi.FailureProtocol {
		t.Fatalf("DoJSON() error = %T %v, want protocol Failure", err, err)
	}
}

func newClient(t *testing.T, config clientapi.Config) *clientapi.Client {
	t.Helper()
	client, err := clientapi.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func startServer(t *testing.T, runtimeDirectory string) *localapi.Server {
	t.Helper()
	server, err := localapi.NewServer(runtimeDirectory)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func endpointPort(t *testing.T, server *localapi.Server) int {
	t.Helper()
	endpoint, ok := server.Endpoint()
	if !ok {
		t.Fatal("server endpoint is unavailable")
	}
	return endpoint.Port
}

func absoluteTempDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatalf("Abs(temp dir) error = %v", err)
	}
	return directory
}
