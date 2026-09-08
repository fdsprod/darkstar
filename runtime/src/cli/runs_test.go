package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	runCLIJSON(t, []string{"run", "prepare", run.WorkItemID, "--answers-json", `{"goal":"Implement the reviewed design"}`, "--inputs-json", `{"request":{"approved":true}}`, "--evidence", "docs/plan.md", "--json"}, &prepared)
	if runs.create.Preparation == nil || runs.create.Preparation.Answers["goal"] != "Implement the reviewed design" || string(runs.create.Preparation.RunInputs["request"]) != `{"approved":true}` || len(runs.create.Preparation.Evidence) != 1 || runs.create.Preparation.Evidence[0] != "docs/plan.md" || runs.create.WorkflowID != "" {
		t.Fatalf("preparation inputs lost: %#v", runs.create)
	}
	digest := strings.Repeat("a", 64)
	runCLIJSON(t, []string{"run", "launch", run.RunID, "--if-match", "7", "--confirm-assessment", digest, "--json"}, &launched)
	if runs.control.ConfirmationDigest != digest {
		t.Fatalf("confirmation lost: %#v", runs.control)
	}

}

func TestParseRunPrepareAndLaunchRejectAmbiguousArguments(t *testing.T) {
	workID := "work_00000000000000000000000000"
	request, key, err := parseRunPrepare([]string{workID, "--profile", "implementation", "--idempotency-key", "prepare-key"})
	if err != nil || request.Profile != "implementation" || request.WorkflowID != "" || key != "prepare-key" {
		t.Fatalf("prepare parse = %#v key=%q err=%v", request, key, err)
	}
	if _, _, err := parseRunPrepare([]string{"--scenario", "fake-success"}); err == nil {
		t.Fatal("prepare accepted fake scenario")
	}
	override, _, err := parseRunPrepare([]string{workID, "--entry-node", "design", "--terminal-node", "verify", "--terminal-node", "deliver"})
	if err != nil || override.Preparation == nil || override.Preparation.RouteOverride == nil || override.Preparation.RouteOverride.From != "design" || len(override.Preparation.RouteOverride.Until) != 2 {
		t.Fatalf("route override parse = %#v, err=%v", override, err)
	}
	if _, _, _, _, err := parseRunLaunch([]string{"run_00000000000000000000000000"}); err == nil {
		t.Fatal("launch accepted missing --if-match")
	}
	if _, _, _, _, err := parseRunLaunch([]string{"run_00000000000000000000000000", "--if-match", "0"}); err == nil {
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

func TestPreparationArgumentsRejectMalformedObjectsAndConfirmation(t *testing.T) {
	for _, args := range [][]string{
		{"--answers-json", "null"}, {"--answers-json", "[]"}, {"--answers-json", `{"x":1}`},
		{"--answers-json", "{}", "--answers-json", "{}"}, {"--inputs-json", "null"},
		{"--inputs-json", `{"bad-name":true}`}, {"--inputs-json", "{}", "--inputs-json", "{}"},
		{"--profile", "delivery", "--entry-node", "design"}, {"--entry-node", "design", "--entry-node", "verify"},
	} {
		if _, _, err := parseRunPrepare(append([]string{"work_00000000000000000000000000"}, args...)); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	for _, digest := range []string{"short", strings.Repeat("A", 64)} {
		if _, _, _, _, err := parseRunLaunch([]string{"run_00000000000000000000000000", "--if-match", "1", "--confirm-assessment", digest}); err == nil {
			t.Errorf("accepted digest %s", digest)
		}
	}
}

func TestPreparedRunTextShowsActionableAssessment(t *testing.T) {
	run := statestore.RunProjection{RunID: "run_00000000000000000000000000", Status: statestore.RunWaiting, RouteSnapshot: statestore.JSONSnapshot(`{"assessment":{"digest":"abc","rationale":"More detail is required.","questions":[{"id":"outcome","prompt":"What should be delivered?"}],"confirmationReasons":["Review scope."]}}`)}
	var stdout, stderr bytes.Buffer
	code := writeRunProjectionActionResult(run, "Prepared", false, &stdout, &stderr, "darkstar run prepare")
	if code != 0 || !strings.Contains(stdout.String(), "Input outcome: What should be delivered?") || !strings.Contains(stdout.String(), "Digest: abc") || !strings.Contains(stdout.String(), "Confirmation: Review scope.") {
		t.Fatalf("code=%d output=%s error=%s", code, stdout.String(), stderr.String())
	}
}
