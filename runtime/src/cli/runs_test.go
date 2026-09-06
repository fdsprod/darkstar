package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	localapi "darkstar/src/api"
	"darkstar/src/core/runexecution"
	platformport "darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

func TestRunPrepareAndLaunchUseDurableReadyAPI(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{
		Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"),
		Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
	}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalResolver := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) { return paths, nil }
	t.Cleanup(func() { resolveApplicationPaths = originalResolver })

	run := statestore.RunProjection{
		RunID: "run_00000000000000000000000000", WorkItemID: "work_00000000000000000000000000",
		WorkflowID: "darkstar/story-execution", WorkflowVersion: "1.4.0", Status: statestore.RunReady,
		ResourceVersion: 7, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	runs := &cliRecordingRunService{run: run}
	server, err := localapi.NewServer(paths.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetRuns(runs); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close API server: %v", err)
		}
	})

	var prepared runMachineOutput
	runCLIJSON(t, []string{"run", "prepare", run.WorkItemID, "--workflow", run.WorkflowID, "--version", run.WorkflowVersion, "--profile", "implementation", "--idempotency-key", "cli-prepare-run", "--json"}, &prepared)
	if runs.prepared != 1 || runs.create.Profile != "implementation" || runs.create.WorkItemID != run.WorkItemID || runs.key != "cli-prepare-run" {
		t.Fatalf("prepare calls=%d request=%#v key=%q", runs.prepared, runs.create, runs.key)
	}

	var launched runMachineOutput
	runCLIJSON(t, []string{"run", "launch", run.RunID, "--if-match", "7", "--idempotency-key", "cli-launch-run", "--json"}, &launched)
	if runs.launched != 1 || runs.control.RunID != run.RunID || runs.control.ExpectedResourceVersion != 7 || runs.control.IdempotencyKey != "cli-launch-run" {
		t.Fatalf("launch calls=%d request=%#v", runs.launched, runs.control)
	}
}

func TestParseRunPrepareAndLaunchRejectAmbiguousArguments(t *testing.T) {
	workID := "work_00000000000000000000000000"
	request, key, err := parseRunPrepare([]string{workID, "--profile", "implementation", "--idempotency-key", "prepare-key"})
	if err != nil || request.Profile != "implementation" || request.WorkflowID != runexecution.DefaultWorkflowID || key != "prepare-key" {
		t.Fatalf("prepare parse = %#v key=%q err=%v", request, key, err)
	}
	if _, _, err := parseRunPrepare([]string{"--scenario", "fake-success"}); err == nil {
		t.Fatal("prepare accepted fake scenario")
	}
	if _, _, _, err := parseRunLaunch([]string{"run_00000000000000000000000000"}); err == nil {
		t.Fatal("launch accepted missing --if-match")
	}
	if _, _, _, err := parseRunLaunch([]string{"run_00000000000000000000000000", "--if-match", "0"}); err == nil {
		t.Fatal("launch accepted zero revision")
	}
}

type cliRecordingRunService struct {
	run                statestore.RunProjection
	prepared, launched int
	create             runexecution.CreateRequest
	control            runexecution.ControlRequest
	key                string
}

func (s *cliRecordingRunService) Create(context.Context, runexecution.CreateRequest, string) (statestore.RunProjection, error) {
	return s.run, nil
}
func (s *cliRecordingRunService) Prepare(_ context.Context, request runexecution.CreateRequest, key string) (statestore.RunProjection, error) {
	s.prepared++
	s.create, s.key = request, key
	return s.run, nil
}
func (s *cliRecordingRunService) Start(context.Context, runexecution.StartRequest, string) (runexecution.View, error) {
	return runexecution.View{}, errors.New("not used")
}
func (s *cliRecordingRunService) List(context.Context, int, string) (runexecution.Page, error) {
	return runexecution.Page{}, errors.New("not used")
}
func (s *cliRecordingRunService) Get(context.Context, string) (runexecution.View, error) {
	return runexecution.View{}, errors.New("not used")
}
func (s *cliRecordingRunService) Launch(_ context.Context, request runexecution.ControlRequest) (statestore.RunProjection, error) {
	s.launched++
	s.control = request
	return s.run, nil
}
func (s *cliRecordingRunService) Pause(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error) {
	return s.run, errors.New("not used")
}
func (s *cliRecordingRunService) Resume(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error) {
	return s.run, errors.New("not used")
}
func (s *cliRecordingRunService) Retry(context.Context, runexecution.RetryRequest) (statestore.RunProjection, error) {
	return s.run, errors.New("not used")
}
func (s *cliRecordingRunService) Continue(context.Context, runexecution.ContinueRequest) (statestore.RunProjection, error) {
	return s.run, errors.New("not used")
}
func (s *cliRecordingRunService) Cancel(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error) {
	return s.run, errors.New("not used")
}
