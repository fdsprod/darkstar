package repositoryscope_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/core/investigation"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/repositorysnapshot"
	"darkstar/src/ports/statestore"
)

type investigationWorker struct {
	mu              sync.Mutex
	calls           map[string]int
	reconcile       int
	active          int
	maximum         int
	failRepo        string
	block           bool
	uncertainCancel bool
}

func TestInvestigationSharedDynamicAdmission(t *testing.T) {
	db, _, scopes, _, project := fixture(t, 0)
	ctx := context.Background()
	scope, err := scopes.Prepare(ctx, request(project), "admission-scope")
	if err != nil {
		t.Fatal(err)
	}
	var limit atomic.Int64
	limit.Store(1)
	var shared sync.Mutex
	worker := &investigationWorker{}
	service, err := investigation.New(db, scopes, worker, investigation.Options{OwnerID: "admission-owner", GlobalConcurrency: 1, GlobalLimit: func() (int, error) {
		return int(limit.Load()), nil
	}, Admission: &shared, OtherActive: func(context.Context) (int, error) {
		return 1, nil
	}, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	view, err := service.Prepare(ctx, investigation.PrepareRequest{ScopeID: scope.Scope.ScopeID, Task: investigation.TaskInput{Kind: "text", Text: "Plan"}}, "admission-collection")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(ctx, view.Collection.CollectionID, 1, "start"); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = service.Run(ctx)
	}()
	time.Sleep(100 * time.Millisecond)
	before, err := service.Get(ctx, view.Collection.CollectionID)
	if err != nil || len(before.Attempts) != 0 {
		t.Fatalf("other workflow slot was ignored: %#v %v", before, err)
	}
	limit.Store(2)
	waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.Collection.Status == "succeeded"
	})
}

func TestInvestigationSchedulerRecoversTransientAdmissionError(t *testing.T) {
	db, _, scopes, _, project := fixture(t, 0)
	ctx := context.Background()
	scope, err := scopes.Prepare(ctx, request(project), "transient-scope")
	if err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Bool
	unavailable.Store(true)
	service, err := investigation.New(db, scopes, &investigationWorker{}, investigation.Options{OwnerID: "transient-owner", PollInterval: 5 * time.Millisecond, GlobalLimit: func() (int, error) {
		if unavailable.Load() {
			return 0, errors.New("configuration edit in progress")
		}
		return 1, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	view, err := service.Prepare(ctx, investigation.PrepareRequest{ScopeID: scope.Scope.ScopeID, Task: investigation.TaskInput{Kind: "text", Text: "Plan after configuration settles"}}, "transient-collection")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(ctx, view.Collection.CollectionID, 1, "start"); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = service.Run(ctx)
	}()
	blocked := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.SchedulerError != ""
	})
	if len(blocked.Attempts) != 0 || blocked.Collection.Status != "running" {
		t.Fatalf("admitted work while configuration unavailable: %#v", blocked)
	}
	unavailable.Store(false)
	finished := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.Collection.Status == "succeeded" && v.SchedulerError == ""
	})
	if len(finished.Attempts) != 1 {
		t.Fatalf("transient recovery reran work: %#v", finished)
	}
}

func TestInvestigationUsesAvailableSubsetOfBlockedScope(t *testing.T) {
	db, _, scopes, exports, project := fixture(t, 2)
	ctx := context.Background()
	exports.fail = true
	scope, err := scopes.Prepare(ctx, request(project), "partial-scope")
	if err != nil || scope.Preparation.Status != statestore.RepositoryScopeBlocked {
		t.Fatalf("blocked scope %#v %v", scope, err)
	}
	exports.fail = false
	first := scope.Scope.Repositories[0]
	evidence, err := exports.Materialize(ctx, repositorysnapshot.ExportRequest{ScopeID: scope.Scope.ScopeID, RepositoryID: first.Repository.RepositoryID, Root: first.Repository.Root, CommonGitDir: first.Repository.CommonGitDir, CommitSHA: first.Revision.CommitSHA, PathScope: []string{"src"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveRepositoryScopeEvidence(ctx, statestore.RepositoryScopeEvidence{ScopeID: scope.Scope.ScopeID, RepositoryID: first.Repository.RepositoryID, Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	worker := &investigationWorker{}
	service, err := investigation.New(db, scopes, worker, investigation.Options{OwnerID: "partial-owner", GlobalConcurrency: 2, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	view, err := service.Prepare(ctx, investigation.PrepareRequest{ScopeID: scope.Scope.ScopeID, Task: investigation.TaskInput{Kind: "text", Text: "Inspect available evidence"}}, "partial-collection")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(ctx, view.Collection.CollectionID, 1, "start"); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = service.Run(ctx)
	}()
	partial := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.Collection.Status == "partial"
	})
	successes, missing := 0, 0
	for _, unit := range partial.Units {
		if unit.Kind == "repository" && unit.Status == "succeeded" {
			successes++
		}
		if unit.Kind == "repository" && unit.Status == "failed" {
			missing++
		}
	}
	if successes != 1 || missing != 1 {
		t.Fatalf("partial scope did not preserve available evidence: %#v", partial)
	}
}

func (w *investigationWorker) ResolveTask(_ context.Context, input investigation.TaskInput, _ string) (investigation.FrozenTask, error) {
	raw, _ := json.Marshal(map[string]string{"text": input.Text})
	return investigation.FrozenTask{Kind: input.Kind, Content: raw, Digest: statestore.RepositoryScopeContentDigest(json.RawMessage(raw))}, nil
}

func (w *investigationWorker) ResolveProvider(context.Context, string) (investigation.ProviderSelection, error) {
	return investigation.ProviderSelection{Provider: "test", CapabilityFingerprint: "test-v1", Extension: &extension.Ref{ID: "darkstar/test", Version: "1.0.0", Digest: strings.Repeat("c", 64)}}, nil
}

func (w *investigationWorker) Execute(ctx context.Context, input investigation.Execution, recorder investigation.Recorder) investigation.Outcome {
	w.mu.Lock()
	if w.calls == nil {
		w.calls = map[string]int{}
	}
	repo := "synthesis"
	if len(input.Scope.Repositories) > 0 {
		repo = input.Scope.Repositories[0].Repository.RepositoryID
	}
	w.calls[repo]++
	count := w.calls[repo]
	w.active++
	if w.active > w.maximum {
		w.maximum = w.active
	}
	block := w.block
	fail := repo == w.failRepo && count == 1
	uncertain := w.uncertainCancel
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.active--
		w.mu.Unlock()
	}()
	prepared := json.RawMessage(`{}`)
	if err := recorder.RecordPrepared(context.Background(), "test-v1", statestore.RepositoryScopeContentDigest(prepared), prepared); err != nil {
		return investigation.Outcome{State: "failed", Reason: err.Error()}
	}
	if block {
		<-ctx.Done()
		state := "cancelled"
		if uncertain {
			state = "uncertain"
		}
		return investigation.Outcome{State: state, Reason: "provider cancellation observed"}
	}
	time.Sleep(30 * time.Millisecond)
	if fail {
		return investigation.Outcome{State: "failed", Reason: "selected evidence unavailable"}
	}
	result := workerResult(input)
	return investigation.Outcome{State: "succeeded", Result: &result}
}

func (w *investigationWorker) Reconcile(_ context.Context, input investigation.Execution, attempt investigation.Attempt, _ investigation.Recorder) investigation.Outcome {
	w.mu.Lock()
	w.reconcile++
	w.mu.Unlock()
	if input.CancelRequested {
		return investigation.Outcome{State: "cancelled", Reason: "confirmed cancellation of retained attempt"}
	}
	if attempt.Result != nil {
		return investigation.Outcome{State: "succeeded", Result: attempt.Result}
	}
	return investigation.Outcome{State: "uncertain", Reason: "provider start outcome is unknown"}
}

func workerResult(input investigation.Execution) investigation.UnitResult {
	repo := ""
	if len(input.Scope.Repositories) > 0 {
		repo = input.Scope.Repositories[0].Repository.RepositoryID
	}
	quality := "complete"
	if len(input.Missing) > 0 {
		quality = "partial"
	}
	return investigation.UnitResult{Kind: input.Kind, RepositoryID: repo, Quality: quality, Findings: json.RawMessage(`{"summary":"validated evidence"}`), Artifact: artifactregistry.VersionRef{ArtifactID: identity.Deterministic("artifact_", input.AttemptID), Version: 1}, Digest: strings.Repeat("b", 64)}
}

func waitInvestigation(t *testing.T, service *investigation.Service, id string, done func(investigation.View) bool) investigation.View {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var latest investigation.View
	for time.Now().Before(deadline) {
		view, err := service.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		latest = view
		if done(view) {
			return view
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("investigation did not settle: %#v", latest)
	return latest
}

func TestInvestigationBoundedRetryRetainsSuccessAndResynthesizes(t *testing.T) {
	db, _, scopes, _, project := fixture(t, 3)
	ctx := context.Background()
	scope, err := scopes.Prepare(ctx, request(project), "investigation-scope")
	if err != nil {
		t.Fatal(err)
	}
	worker := &investigationWorker{failRepo: project.Repositories[0].Repository.RepositoryID}
	service, err := investigation.New(db, scopes, worker, investigation.Options{OwnerID: "daemon-a", GlobalConcurrency: 2, PollInterval: time.Millisecond * 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	view, err := service.Prepare(ctx, investigation.PrepareRequest{ScopeID: scope.Scope.ScopeID, Task: investigation.TaskInput{Kind: "text", Text: "Investigate interfaces"}, Concurrency: 2}, "collection-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(ctx, view.Collection.CollectionID, view.Collection.Revision, "start-collection"); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = service.Run(ctx)
	}()
	partial := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.Collection.Status == "partial"
	})
	successful := map[string]string{}
	for _, unit := range partial.Units {
		if unit.Kind == "repository" && unit.Status == "succeeded" {
			successful[unit.RepositoryID] = unit.CurrentAttemptID
		}
	}
	if len(successful) != 2 {
		t.Fatalf("expected two retained successes: %#v", partial)
	}
	if _, err = service.Retry(ctx, view.Collection.CollectionID, partial.Collection.Revision, "retry-failed-only"); err != nil {
		t.Fatal(err)
	}
	finished := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.Collection.Status == "succeeded"
	})
	if len(finished.Attempts) != 6 {
		t.Fatalf("attempt count=%d want3 initial repos+2synthesis+1retry", len(finished.Attempts))
	}
	for _, unit := range finished.Units {
		if id := successful[unit.RepositoryID]; id != "" && id != unit.CurrentAttemptID {
			t.Fatal("successful repository was rerun")
		}
	}
	worker.mu.Lock()
	maximum := worker.maximum
	worker.mu.Unlock()
	if maximum != 2 {
		t.Fatalf("bounded worker maximum=%d", maximum)
	}
	replay, err := service.Start(ctx, view.Collection.CollectionID, 1, "start-collection")
	if err != nil || replay.Collection.Status != "succeeded" {
		t.Fatalf("command replay=%#v %v", replay, err)
	}
}

func TestInvestigationCancellationUncertaintyBlocksRetryAndRecovers(t *testing.T) {
	db, _, scopes, _, project := fixture(t, 1)
	ctx := context.Background()
	scope, err := scopes.Prepare(ctx, request(project), "cancel-scope")
	if err != nil {
		t.Fatal(err)
	}
	worker := &investigationWorker{block: true, uncertainCancel: true}
	service, err := investigation.New(db, scopes, worker, investigation.Options{OwnerID: "daemon-cancel", GlobalConcurrency: 1, PollInterval: 5 * time.Millisecond, LeaseDuration: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	view, err := service.Prepare(ctx, investigation.PrepareRequest{ScopeID: scope.Scope.ScopeID, Task: investigation.TaskInput{Kind: "text", Text: "Inspect"}}, "cancel-collection")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(ctx, view.Collection.CollectionID, 1, "start"); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = service.Run(ctx)
	}()
	running := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return len(v.Attempts) == 1 && v.Attempts[0].ContextDigest != ""
	})
	if _, err = service.Cancel(ctx, view.Collection.CollectionID, running.Collection.Revision, "cancel"); err != nil {
		t.Fatal(err)
	}
	uncertain := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return len(v.Attempts) == 1 && v.Attempts[0].State == "uncertain"
	})
	if uncertain.Collection.Status != "cancelling" {
		t.Fatal("uncertain cancellation claimed completion")
	}
	if _, err = service.Retry(ctx, view.Collection.CollectionID, uncertain.Collection.Revision, "unsafe-retry"); !errors.Is(err, investigation.ErrConflict) {
		t.Fatalf("uncertain attempt redispatched: %v", err)
	}
	if _, err = db.SQL().ExecContext(ctx, `UPDATE investigation_attempts SET record_json=json_set(record_json,'$.leaseExpiresAt','2000-01-01T00:00:00Z') WHERE attempt_id=?`, uncertain.Attempts[0].AttemptID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.Collection.Status == "cancelled"
	})
	if len(cancelled.Attempts) != 1 {
		t.Fatal("cancel reconciliation started another attempt")
	}
}

func TestInvestigationDurableClaimAndRecoveryNeverRedispatchSuccess(t *testing.T) {
	db, _, scopes, _, project := fixture(t, 0)
	ctx := context.Background()
	scope, err := scopes.Prepare(ctx, request(project), "none-scope")
	if err != nil {
		t.Fatal(err)
	}
	worker := &investigationWorker{}
	service, err := investigation.New(db, scopes, worker, investigation.Options{OwnerID: "recovery-owner", GlobalConcurrency: 1, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	view, err := service.Prepare(ctx, investigation.PrepareRequest{ScopeID: scope.Scope.ScopeID, Task: investigation.TaskInput{Kind: "text", Text: "Plan no code"}}, "none-collection")
	if err != nil {
		t.Fatal(err)
	}
	if view.Collection.Provider.Provider != "daemon" {
		t.Fatal("no-code preparation selected external provider")
	}
	if _, err = service.Start(ctx, view.Collection.CollectionID, 1, "start-none"); err != nil {
		t.Fatal(err)
	}
	claim, found, err := db.ClaimInvestigation(ctx, "crashed-owner", 1, 3*time.Second)
	if err != nil || !found {
		t.Fatalf("claim %v %v", found, err)
	}
	if _, found, err = db.ClaimInvestigation(ctx, "other-owner", 1, 3*time.Second); err != nil || found {
		t.Fatalf("global reservation lost: %v %v", found, err)
	}
	if err = db.RenewInvestigationAttempt(ctx, claim.Attempt.AttemptID, "wrong-owner", time.Second); !errors.Is(err, statestore.ErrRepositoryScopeConflict) {
		t.Fatalf("foreign owner renewed: %v", err)
	}
	result := workerResult(investigation.Execution{Kind: "synthesis", AttemptID: claim.Attempt.AttemptID})
	prepared := json.RawMessage(`{}`)
	if err = db.ObserveInvestigationAttempt(ctx, claim.Attempt.AttemptID, "crashed-owner", statestore.InvestigationObservation{Kind: "prepared", Request: prepared, ContextDigest: statestore.RepositoryScopeContentDigest(prepared), CapabilityFingerprint: "test-v1"}); err != nil {
		t.Fatal(err)
	}
	if err = db.ObserveInvestigationAttempt(ctx, claim.Attempt.AttemptID, "crashed-owner", statestore.InvestigationObservation{Kind: "result", Result: &result}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL().ExecContext(ctx, `UPDATE investigation_attempts SET record_json=json_set(record_json,'$.leaseExpiresAt','2000-01-01T00:00:00Z') WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = service.Run(ctx)
	}()
	finished := waitInvestigation(t, service, view.Collection.CollectionID, func(v investigation.View) bool {
		return v.Collection.Status == "succeeded"
	})
	worker.mu.Lock()
	calls := fmt.Sprint(worker.calls)
	reconciles := worker.reconcile
	worker.mu.Unlock()
	if len(finished.Attempts) != 1 || calls != "map[]" || reconciles != 1 {
		t.Fatalf("recovery redispatched: %#v calls%s reconciliation%d", finished, calls, reconciles)
	}
	if err = db.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := service.Get(ctx, view.Collection.CollectionID)
	if err != nil || len(after.Attempts) != 1 || after.Attempts[0].Result.Artifact != result.Artifact {
		t.Fatalf("retained result lost on replay: %#v %v", after, err)
	}
}
