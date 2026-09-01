package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localapi "github.com/fdsprod/darkstar/runtime/src/api"
	"github.com/fdsprod/darkstar/runtime/src/core/runexecution"
	"github.com/fdsprod/darkstar/runtime/src/daemon"
	platformport "github.com/fdsprod/darkstar/runtime/src/ports/platform"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

func TestPublicCLIFakeRunSurvivesRestartWithoutDuplicateEffectsAndExports(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{
		Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"),
		Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
	}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalResolver := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) { return paths, nil }
	t.Cleanup(func() { resolveApplicationPaths = originalResolver })

	first := startAcceptanceService(t, paths, "11111111111111111111111111111111")
	var start runexecution.View
	runCLIJSON(t, []string{"run", "start", "--scenario", runexecution.ScenarioRestart, "--idempotency-key", "acceptance-restart", "--json"}, &start)
	if start.Run.Status != statestore.RunPending || len(start.Attempts) != 1 || start.Attempts[0].Status != statestore.AttemptStarting {
		t.Fatalf("start response = %#v", start)
	}

	waitForRun(t, start.Run.RunID, func(view runexecution.View) bool {
		return view.Run.Status == statestore.RunRunning && len(view.Attempts) == 1 &&
			view.Attempts[0].Status == statestore.AttemptRunning && view.Attempts[0].LastSequence == 1
	})
	if err := first.Close(); err != nil {
		t.Fatalf("stop first daemon service: %v", err)
	}

	second := startAcceptanceService(t, paths, "22222222222222222222222222222222")
	t.Cleanup(func() { _ = second.Close() })
	var watched runexecution.View
	runCLIJSON(t, []string{"run", "watch", start.Run.RunID, "--json"}, &watched)
	if watched.Run.Status != statestore.RunCompleted || len(watched.Attempts) != 1 ||
		watched.Attempts[0].Status != statestore.AttemptCompleted || watched.Attempts[0].LastSequence != 3 {
		t.Fatalf("watched run = %#v", watched)
	}

	var replayed runexecution.View
	runCLIJSON(t, []string{"run", "start", "--scenario", runexecution.ScenarioRestart, "--idempotency-key", "acceptance-restart", "--json"}, &replayed)
	if replayed.Run.RunID != start.Run.RunID || replayed.Run.Status != statestore.RunCompleted {
		t.Fatalf("idempotent replay = %#v, original run %s", replayed, start.Run.RunID)
	}

	bundle := filepath.Join(root, "run.zip")
	var exported runExportOutput
	runCLIJSON(t, []string{"run", "export", start.Run.RunID, "--output", bundle, "--json"}, &exported)
	if exported.RunID != start.Run.RunID || exported.Size <= 0 {
		t.Fatalf("export response = %#v", exported)
	}
	events := readZipEntry(t, bundle, "events.jsonl")
	for kind, want := range map[string]int{
		`"kind":"attempt.started"`: 1, `"kind":"attempt.resumed"`: 1,
		`"kind":"attempt.provider_event"`: 3, `"kind":"attempt.completed"`: 1, `"kind":"run.completed"`: 1,
	} {
		if got := strings.Count(events, kind); got != want {
			t.Errorf("%s count = %d, want %d\n%s", kind, got, want, events)
		}
	}
	commands := readZipEntry(t, bundle, "commands.json")
	if strings.Count(commands, `"idempotencyKey": "acceptance-restart"`) != 1 {
		t.Fatalf("command evidence does not contain exactly one start command: %s", commands)
	}
}

func startAcceptanceService(t *testing.T, paths platformport.Paths, instanceID string) *daemonAPIService {
	t.Helper()
	server, err := localapi.NewServer(paths.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	service := &daemonAPIService{server: server, paths: paths, projectRoot: filepath.Dir(paths.Data)}
	state := daemon.State{SchemaVersion: 1, InstanceID: instanceID, Process: daemon.ProcessIdentity{
		PID: os.Getpid(), StartedAt: time.Now().UTC(), Executable: filepath.Join(paths.Runtime, "darkstar-test.exe"),
	}}
	if err := service.Start(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	return service
}

func runCLIJSON(t *testing.T, args []string, destination any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != int(ExitSuccess) {
		t.Fatalf("Run(%q) code=%d stderr=%s stdout=%s", args, code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), destination); err != nil {
		t.Fatalf("decode Run(%q) output %q: %v", args, stdout.String(), err)
	}
}

func waitForRun(t *testing.T, runID string, ready func(runexecution.View) bool) runexecution.View {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var view runexecution.View
		runCLIJSON(t, []string{"run", "show", runID, "--json"}, &view)
		if ready(view) {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not reach expected state; last=%#v", runID, view)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readZipEntry(t *testing.T, archivePath, name string) string {
	t.Helper()
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	t.Fatalf("ZIP entry %s not found", name)
	return ""
}
