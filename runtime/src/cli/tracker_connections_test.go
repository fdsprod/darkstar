package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"darkstar/src/adapters/tracker/localconnection"
	"darkstar/src/ports/platform"
	"darkstar/src/ports/trackerconnection"
)

func TestTrackerCredentialCLIStoresOnlyProtectedStdinValue(t *testing.T) {
	root := t.TempDir()
	paths := platform.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalPaths := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platform.Paths, error) {
		return paths, nil
	}
	t.Cleanup(func() {
		resolveApplicationPaths = originalPaths
	})
	service := startAcceptanceService(t, paths, "66666666666666666666666666666666")
	t.Cleanup(func() {
		_ = service.Close()
	})
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	const secret = "cli-secret-from-stdin-only"
	if _, err := writer.WriteString(secret + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	originalStdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = originalStdin
		_ = reader.Close()
	})
	var stdout, stderr bytes.Buffer
	code := runTracker([]string{"credential", "store", "cli-ref", "--stdin"}, true, &stdout, &stderr)
	if code != 0 || bytes.Contains(stdout.Bytes(), []byte(secret)) || bytes.Contains(stderr.Bytes(), []byte(secret)) {
		t.Fatalf("credential command failed or exposed a value; exit %d", code)
	}
	credentials, err := localconnection.NewCredentials(filepath.Join(paths.Data, "tracker", "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := credentials.Resolve(context.Background(), "cli-ref")
	if err != nil || resolved != secret {
		t.Fatal("daemon did not retain the protected stdin credential")
	}
	var response struct {
		Result struct {
			SchemaVersion int                        `json:"schemaVersion"`
			Connections   []trackerconnection.Record `json:"connections"`
		} `json:"result"`
	}
	runCLIJSON(t, []string{"tracker", "connection", "list", "--json"}, &response)
	if response.Result.SchemaVersion != 1 || response.Result.Connections == nil || len(response.Result.Connections) != 0 {
		t.Fatal("credential storage created a source connection or returned unversioned data")
	}
}
