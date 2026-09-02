package attemptexecution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"darkstar/src/ports/executor"
)

func TestRunnerExecutesAndCommitsValidatedAttempt(t *testing.T) {
	t.Parallel()
	trace := []string{}
	execution := &fakeExecution{
		trace:     &trace,
		reference: executor.Reference{AttemptID: "attempt_1", ExternalID: "external_1", RecoveryRef: "recovery_1"},
		events: &fakeEvents{trace: &trace, values: []executor.Event{
			{Cursor: "1", Kind: "progress", OccurredAt: time.Unix(1, 0).UTC(), Data: json.RawMessage(`{"percent":50}`)},
			{Cursor: "2", Kind: "completed", OccurredAt: time.Unix(2, 0).UTC(), Data: json.RawMessage(`{"percent":100}`)},
		}},
		result: executor.Result{CandidateOutput: json.RawMessage(`{"answer":42}`), RecoveryRef: "recovery_1",
			Artifacts: []executor.Artifact{{Role: "result", Locator: "artifact/result", Digest: "digest", MediaType: "application/json"}}},
	}
	journal := &fakeJournal{trace: &trace}
	runner := newFakeRunner(t, &trace, execution, journal, nil)
	runner.validator.(*fakeValidator).result = ValidationResult{Evidence: []executor.Evidence{{Kind: "validation", Ref: "evidence/validation", Digest: "digest"}}}

	commit, err := runner.Run(context.Background(), startRequest())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if commit.AttemptID != "attempt_1" || commit.LastEventCursor != "2" || string(commit.Result.CandidateOutput) != `{"answer":42}` {
		t.Fatalf("commit = %#v", commit)
	}
	if len(commit.Result.Evidence) != 1 || commit.Result.Evidence[0].Kind != "validation" {
		t.Fatalf("validation evidence was not included in commit: %#v", commit.Result.Evidence)
	}
	if len(journal.commits) != 1 || len(journal.failures) != 0 {
		t.Fatalf("journal commits/failures = %d/%d", len(journal.commits), len(journal.failures))
	}
	wantTrace := []string{
		"resources.acquire", "journal.resources", "context.build", "executor.resolve", "executor.start", "journal.started",
		"events.receive", "journal.event:1", "events.receive", "journal.event:2", "events.receive", "events.close",
		"execution.wait", "validator.validate", "journal.commit", "resources.release:committed",
	}
	if !reflect.DeepEqual(trace, wantTrace) {
		t.Fatalf("trace = %#v\nwant  = %#v", trace, wantTrace)
	}
}

func TestRunnerRejectsInvalidOutputBeforeAtomicCommit(t *testing.T) {
	t.Parallel()
	trace := []string{}
	validationErr := errors.New("schema mismatch")
	execution := &fakeExecution{
		trace:     &trace,
		reference: executor.Reference{AttemptID: "attempt_1", ExternalID: "external_1", RecoveryRef: "recovery_1"},
		events:    &fakeEvents{trace: &trace},
		result:    executor.Result{CandidateOutput: json.RawMessage(`{"answer":"wrong"}`), RecoveryRef: "recovery_1"},
	}
	journal := &fakeJournal{trace: &trace}
	runner := newFakeRunner(t, &trace, execution, journal, validationErr)

	_, err := runner.Run(context.Background(), startRequest())
	if !errors.Is(err, validationErr) {
		t.Fatalf("Run() error = %v, want validation error", err)
	}
	if len(journal.commits) != 0 || len(journal.failures) != 1 || journal.failures[0].Stage != FailureValidation {
		t.Fatalf("journal commits/failures = %#v/%#v", journal.commits, journal.failures)
	}
	if got := trace[len(trace)-1]; got != "resources.release:failed" {
		t.Fatalf("last trace = %q", got)
	}
}

func TestRunnerResumesExactFrozenInvocation(t *testing.T) {
	t.Parallel()
	trace := []string{}
	execution := &fakeExecution{
		trace:     &trace,
		reference: executor.Reference{AttemptID: "attempt_1", ExternalID: "external_1", RecoveryRef: "recovery_1"},
		events:    &fakeEvents{trace: &trace},
		result:    executor.Result{CandidateOutput: json.RawMessage(`{"answer":42}`), RecoveryRef: "recovery_1"},
	}
	adapter := &fakeExecutor{trace: &trace, execution: execution}
	journal := &fakeJournal{trace: &trace}
	runner, err := New(
		&fakeResources{trace: &trace}, &fakeContexts{trace: &trace},
		&fakeResolver{trace: &trace, resolved: adapter}, &fakeValidator{trace: &trace}, journal,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := startRequest()
	request.Invocation = Resume{RecoveryRef: "recovery_1", LastEventCursor: "7"}

	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if adapter.resume.AttemptID != request.AttemptID || adapter.resume.InputDigest != "context-digest" ||
		adapter.resume.RecoveryRef != "recovery_1" || adapter.resume.LastEventCursor != "7" {
		t.Fatalf("resume request = %#v", adapter.resume)
	}
	if adapter.start.AttemptID != "" {
		t.Fatalf("start unexpectedly called: %#v", adapter.start)
	}
}

func TestRunnerRejectsDuplicateEventCursor(t *testing.T) {
	t.Parallel()
	trace := []string{}
	execution := &fakeExecution{
		trace:     &trace,
		reference: executor.Reference{AttemptID: "attempt_1", ExternalID: "external_1", RecoveryRef: "recovery_1"},
		events: &fakeEvents{trace: &trace, values: []executor.Event{
			{Cursor: "1", Kind: "progress", OccurredAt: time.Unix(1, 0).UTC(), Data: json.RawMessage(`{}`)},
			{Cursor: "1", Kind: "progress", OccurredAt: time.Unix(2, 0).UTC(), Data: json.RawMessage(`{}`)},
		}},
		result: executor.Result{CandidateOutput: json.RawMessage(`{}`), RecoveryRef: "recovery_1"},
	}
	journal := &fakeJournal{trace: &trace}
	runner := newFakeRunner(t, &trace, execution, journal, nil)

	_, err := runner.Run(context.Background(), startRequest())
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Run() error = %v, want invalid event", err)
	}
	if len(journal.commits) != 0 || len(journal.failures) != 1 || journal.failures[0].Stage != FailureEvents {
		t.Fatalf("journal commits/failures = %#v/%#v", journal.commits, journal.failures)
	}
}

func TestRunnerReportsReleaseFailureAfterCommit(t *testing.T) {
	t.Parallel()
	trace := []string{}
	releaseErr := errors.New("lease release failed")
	execution := &fakeExecution{
		trace:     &trace,
		reference: executor.Reference{AttemptID: "attempt_1", ExternalID: "external_1", RecoveryRef: "recovery_1"},
		events:    &fakeEvents{trace: &trace},
		result:    executor.Result{CandidateOutput: json.RawMessage(`{}`), RecoveryRef: "recovery_1"},
	}
	journal := &fakeJournal{trace: &trace}
	runner, err := New(
		&fakeResources{trace: &trace, releaseErr: releaseErr}, &fakeContexts{trace: &trace},
		&fakeResolver{trace: &trace, resolved: &fakeExecutor{trace: &trace, execution: execution}},
		&fakeValidator{trace: &trace}, journal,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	commit, err := runner.Run(context.Background(), startRequest())
	if !errors.Is(err, releaseErr) {
		t.Fatalf("Run() error = %v, want release failure", err)
	}
	if commit.AttemptID != "attempt_1" || len(journal.commits) != 1 || len(journal.failures) != 0 {
		t.Fatalf("committed result = %#v, journal = %#v", commit, journal)
	}
}

func TestRunnerRecordsCancelledExecutionAsInterrupted(t *testing.T) {
	t.Parallel()
	trace := []string{}
	execution := &fakeExecution{
		trace:     &trace,
		reference: executor.Reference{AttemptID: "attempt_1", ExternalID: "external_1", RecoveryRef: "recovery_1"},
		events:    &fakeEvents{trace: &trace},
		waitErr:   context.Canceled,
	}
	journal := &fakeJournal{trace: &trace}
	runner := newFakeRunner(t, &trace, execution, journal, nil)

	_, err := runner.Run(context.Background(), startRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if len(journal.failures) != 0 || len(journal.interruptions) != 1 || journal.interruptions[0].Stage != FailureResult {
		t.Fatalf("journal failures/interruptions = %#v/%#v", journal.failures, journal.interruptions)
	}
	if got := trace[len(trace)-1]; got != "resources.release:interrupted" {
		t.Fatalf("last trace = %q", got)
	}
}

func startRequest() Request {
	return Request{
		AttemptID: "attempt_1", RunID: "run_1", VisitID: "visit_1", NodeID: "design", ExecutorKind: "fake",
		Resources: ReadOnlyResources{WorkspaceID: "workspace_1"}, Invocation: Start{Timeout: time.Minute},
	}
}

func newFakeRunner(t *testing.T, trace *[]string, execution executor.Execution, journal *fakeJournal, validationErr error) *Runner {
	t.Helper()
	runner, err := New(
		&fakeResources{trace: trace}, &fakeContexts{trace: trace},
		&fakeResolver{trace: trace, resolved: &fakeExecutor{trace: trace, execution: execution}},
		&fakeValidator{trace: trace, err: validationErr}, journal,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runner
}

type fakeResources struct {
	trace      *[]string
	acquireErr error
	releaseErr error
}

func (fake *fakeResources) Acquire(context.Context, string, ResourcePlan) (Allocation, error) {
	*fake.trace = append(*fake.trace, "resources.acquire")
	return Allocation{ID: "allocation_1", Workspace: "workspace/path", WorkspaceDigest: "workspace-digest"}, fake.acquireErr
}

func (fake *fakeResources) Release(_ context.Context, _ Allocation, outcome ReleaseOutcome) error {
	*fake.trace = append(*fake.trace, "resources.release:"+string(outcome))
	return fake.releaseErr
}

type fakeContexts struct{ trace *[]string }

func (fake *fakeContexts) Build(context.Context, Request, Allocation) (ContextSnapshot, error) {
	*fake.trace = append(*fake.trace, "context.build")
	return ContextSnapshot{Digest: "context-digest", Inputs: json.RawMessage(`{"input":true}`), PolicyDigest: "policy-digest"}, nil
}

type fakeResolver struct {
	trace    *[]string
	resolved executor.Executor
}

func (fake *fakeResolver) Resolve(context.Context, string) (executor.Executor, error) {
	*fake.trace = append(*fake.trace, "executor.resolve")
	return fake.resolved, nil
}

type fakeExecutor struct {
	trace     *[]string
	execution executor.Execution
	start     executor.Request
	resume    executor.ResumeRequest
}

func (*fakeExecutor) Kind() string { return "fake" }

func (fake *fakeExecutor) Start(_ context.Context, request executor.Request) (executor.Execution, error) {
	*fake.trace = append(*fake.trace, "executor.start")
	fake.start = request
	return fake.execution, nil
}

func (fake *fakeExecutor) Resume(_ context.Context, request executor.ResumeRequest) (executor.Execution, error) {
	*fake.trace = append(*fake.trace, "executor.resume")
	fake.resume = request
	return fake.execution, nil
}

type fakeExecution struct {
	trace     *[]string
	reference executor.Reference
	events    executor.Events
	result    executor.Result
	waitErr   error
}

func (fake *fakeExecution) Reference() executor.Reference { return fake.reference }
func (fake *fakeExecution) Events() executor.Events       { return fake.events }
func (fake *fakeExecution) Wait(context.Context) (executor.Result, error) {
	*fake.trace = append(*fake.trace, "execution.wait")
	return fake.result, fake.waitErr
}
func (*fakeExecution) Cancel(context.Context, executor.CancelRequest) (executor.CancelResult, error) {
	return executor.CancelResult{}, nil
}

type fakeEvents struct {
	trace  *[]string
	values []executor.Event
	index  int
}

func (fake *fakeEvents) Receive() (executor.Event, error) {
	*fake.trace = append(*fake.trace, "events.receive")
	if fake.index == len(fake.values) {
		return executor.Event{}, io.EOF
	}
	value := fake.values[fake.index]
	fake.index++
	return value, nil
}

func (fake *fakeEvents) Close() error {
	*fake.trace = append(*fake.trace, "events.close")
	return nil
}

type fakeValidator struct {
	trace  *[]string
	err    error
	result ValidationResult
}

func (fake *fakeValidator) Validate(context.Context, ValidationRequest) (ValidationResult, error) {
	*fake.trace = append(*fake.trace, "validator.validate")
	return fake.result, fake.err
}

type fakeJournal struct {
	trace         *[]string
	commits       []Commit
	failures      []FailureRecord
	interruptions []InterruptedRecord
}

func (fake *fakeJournal) ResourcesAcquired(context.Context, string, Allocation) error {
	*fake.trace = append(*fake.trace, "journal.resources")
	return nil
}
func (fake *fakeJournal) Started(context.Context, StartedRecord) error {
	*fake.trace = append(*fake.trace, "journal.started")
	return nil
}
func (fake *fakeJournal) Event(_ context.Context, record EventRecord) error {
	*fake.trace = append(*fake.trace, "journal.event:"+record.Event.Cursor)
	return nil
}
func (fake *fakeJournal) Commit(_ context.Context, commit Commit) error {
	*fake.trace = append(*fake.trace, "journal.commit")
	fake.commits = append(fake.commits, commit)
	return nil
}
func (fake *fakeJournal) Failed(_ context.Context, failure FailureRecord) error {
	*fake.trace = append(*fake.trace, "journal.failed:"+string(failure.Stage))
	fake.failures = append(fake.failures, failure)
	return nil
}
func (fake *fakeJournal) Interrupted(_ context.Context, interruption InterruptedRecord) error {
	*fake.trace = append(*fake.trace, "journal.interrupted:"+string(interruption.Stage))
	fake.interruptions = append(fake.interruptions, interruption)
	return nil
}
