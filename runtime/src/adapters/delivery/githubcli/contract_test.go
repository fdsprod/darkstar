package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

// requiredGitHubDeliveryCoverage is intentionally separate from the scenario
// table. Deleting or renaming a scenario therefore fails the manifest test
// instead of silently reducing the contract/failure matrix.
var requiredGitHubDeliveryCoverage = []string{
	"auth/missing",
	"repositories/fork-base-and-head",
	"repositories/publication-remote-mismatch",
	"branch/missing",
	"branch/owned",
	"branch/unowned",
	"branch/diverged",
	"pull-request/owned",
	"pull-request/unowned",
	"pull-request/ambiguous",
	"pull-request/closed",
	"pull-request/draft",
	"retry/push-after-success",
	"retry/create-after-success",
	"retry/update-after-success",
	"retry/ready-after-success",
	"push-rejection/proven-unchanged",
	"push-rejection/uncertain-remote",
	"push-rejection/diverged-remote",
	"policy/final-non-draft-create",
	"policy/incremental-draft-create",
	"policy/incremental-draft-update",
	"policy/incremental-draft-ready",
	"checks/success",
	"checks/pending",
	"checks/failure",
	"checks/not-configured",
	"review/changes-requested",
	"safety/no-duplicate-external-mutation",
	"safety/no-unsafe-force-refspec",
}

type githubDeliveryScenario struct {
	name string
	run  func(*testing.T)
}

var githubDeliveryScenarios = []githubDeliveryScenario{
	{name: "auth/missing", run: contractMissingAuth},
	{name: "repositories/fork-base-and-head", run: contractForkCoordinates},
	{name: "repositories/publication-remote-mismatch", run: contractRemoteMismatch},
	{name: "branch/missing", run: contractMissingBranch},
	{name: "branch/owned", run: contractOwnedBranch},
	{name: "branch/unowned", run: contractUnownedBranch},
	{name: "branch/diverged", run: contractDivergedBranch},
	{name: "pull-request/owned", run: contractOwnedPullRequest},
	{name: "pull-request/unowned", run: contractUnownedPullRequest},
	{name: "pull-request/ambiguous", run: contractAmbiguousPullRequest},
	{name: "pull-request/closed", run: contractClosedPullRequest},
	{name: "pull-request/draft", run: contractOwnedDraftPullRequest},
	{name: "retry/push-after-success", run: contractRetryPush},
	{name: "retry/create-after-success", run: contractRetryCreate},
	{name: "retry/update-after-success", run: contractRetryUpdate},
	{name: "retry/ready-after-success", run: contractRetryReady},
	{name: "push-rejection/proven-unchanged", run: contractRejectedPushUnchanged},
	{name: "push-rejection/uncertain-remote", run: contractRejectedPushUncertain},
	{name: "push-rejection/diverged-remote", run: contractRejectedPushDiverged},
	{name: "policy/final-non-draft-create", run: contractFinalCreate},
	{name: "policy/incremental-draft-create", run: contractDraftCreate},
	{name: "policy/incremental-draft-update", run: contractDraftUpdate},
	{name: "policy/incremental-draft-ready", run: contractDraftReady},
	{name: "checks/success", run: contractChecksSuccess},
	{name: "checks/pending", run: contractChecksPending},
	{name: "checks/failure", run: contractChecksFailure},
	{name: "checks/not-configured", run: contractChecksNotConfigured},
	{name: "review/changes-requested", run: contractReviewChangesRequested},
	{name: "safety/no-duplicate-external-mutation", run: contractNoDuplicateMutation},
	{name: "safety/no-unsafe-force-refspec", run: contractNoUnsafeForceRefspec},
}

func TestGitHubDeliveryCoverageManifest(t *testing.T) {
	required := make(map[string]struct{}, len(requiredGitHubDeliveryCoverage))
	for _, name := range requiredGitHubDeliveryCoverage {
		if _, duplicate := required[name]; duplicate {
			t.Fatalf("duplicate required coverage entry %q", name)
		}
		required[name] = struct{}{}
	}
	implemented := make(map[string]struct{}, len(githubDeliveryScenarios))
	for _, scenario := range githubDeliveryScenarios {
		if scenario.run == nil {
			t.Errorf("scenario %q has no executable test", scenario.name)
		}
		if _, duplicate := implemented[scenario.name]; duplicate {
			t.Errorf("duplicate scenario %q", scenario.name)
		}
		implemented[scenario.name] = struct{}{}
		if _, expected := required[scenario.name]; !expected {
			t.Errorf("scenario %q is not declared in required coverage", scenario.name)
		}
	}
	for name := range required {
		if _, covered := implemented[name]; !covered {
			t.Errorf("required GitHub delivery coverage %q is missing", name)
		}
	}
}

func TestGitHubDeliveryContractAndFailureMatrix(t *testing.T) {
	for _, scenario := range githubDeliveryScenarios {
		scenario := scenario
		t.Run(scenario.name, scenario.run)
	}
}

func contractMissingAuth(t *testing.T) {
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{err: errors.New("not authenticated")},
	)
	observation, err := operationAdapter(t, runner).ProbeHealth(context.Background(), delivery.HealthRequest{
		LocalRepository: t.TempDir(), RemoteName: "origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := observation.Outcome.(delivery.HealthUnauthenticated); !ok {
		t.Fatalf("outcome = %T", observation.Outcome)
	}
	assertNoExternalMutation(t, runner.calls)
}

func contractForkCoordinates(t *testing.T) {
	request := finalChangeRequest()
	base := delivery.Repository{Provider: Provider, Host: "github.com", Owner: "upstream", Name: "runtime"}
	head := delivery.Repository{Provider: Provider, Host: "github.com", Owner: "contributor", Name: "runtime-fork"}
	request.Coordinates = delivery.ChangeRequestCoordinates{
		Base: delivery.BranchRef{Repository: base, Name: "main"},
		Head: delivery.BranchRef{Repository: head, Name: "codex/dar-94"},
	}
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	pull := pullSpec{number: 94, title: request.Title, body: body, baseRepository: "upstream/runtime", headRepository: "contributor/runtime-fork", headRef: "codex/dar-94"}
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte(`[]`)},
		commandResponse{stdout: []byte(oldCommit + "\n")},
		commandResponse{stdout: []byte(newCommit + "\n")},
		commandResponse{stdout: []byte(`{"number":94}`)},
		commandResponse{stdout: pullResponsesJSON(t, pull)},
	)
	creation, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := creation.Outcome.(delivery.ChangeRequestCreated); !ok {
		t.Fatalf("outcome = %T", creation.Outcome)
	}
	query := url.Values{"base": {"main"}, "head": {"contributor:codex/dar-94"}, "per_page": {"100"}, "state": {"all"}}.Encode()
	wantSearchEndpoint := "repos/upstream/runtime/pulls?" + query
	if runner.calls[0].arguments[3] != wantSearchEndpoint || runner.calls[4].arguments[3] != wantSearchEndpoint {
		t.Fatalf("fork search endpoints = %q / %q", runner.calls[0].arguments[3], runner.calls[4].arguments[3])
	}
	var payload map[string]any
	if err := json.Unmarshal(runner.calls[3].input, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["base"] != "main" || payload["head"] != "contributor:codex/dar-94" || payload["head_repo"] != "runtime-fork" {
		t.Fatalf("fork payload = %#v", payload)
	}
	assertMutationCount(t, runner.calls, mutationPOST, 1)
}

func contractRemoteMismatch(t *testing.T) {
	runner := scriptedRunner(t, commandResponse{stdout: []byte("git@github.com:someone-else/runtime.git\n")})
	_, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	assertFailureCode(t, err, ports.FailureConflict)
	assertNoExternalMutation(t, runner.calls)
}

func contractMissingBranch(t *testing.T) {
	runner := publishRunnerForMissingBranch(t, commandResponse{})
	publication, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := publication.Outcome.(delivery.BranchPublished); !ok {
		t.Fatalf("outcome = %T", publication.Outcome)
	}
	assertOneSafePush(t, runner.calls)
}

func contractOwnedBranch(t *testing.T) {
	owner := delivery.BranchOwnershipEvidence{Owner: testOwner(), EstablishedByOperationID: "publish-original"}
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stdout: []byte(oldCommit + "\n")},
		commandResponse{},
		commandResponse{},
	)
	publication, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.OwnedRemoteBranchAt{CommitSHA: oldCommit, Ownership: owner}))
	if err != nil {
		t.Fatal(err)
	}
	if publication.Ownership != owner {
		t.Fatalf("ownership = %#v", publication.Ownership)
	}
	assertOneSafePush(t, runner.calls)
}

func contractOwnedPullRequest(t *testing.T) {
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 94, title: request.Title, body: body})})
	creation, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := creation.Outcome.(delivery.ChangeRequestReconciled); !ok {
		t.Fatalf("outcome = %T", creation.Outcome)
	}
	assertNoExternalMutation(t, runner.calls)
}

func contractOwnedDraftPullRequest(t *testing.T) {
	request := incrementalDraftChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, request.Intent.(delivery.CreateIncrementalDraft).Content)
	runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 94, title: request.Title, body: body, draft: true})})
	creation, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, ok := creation.Outcome.(delivery.ChangeRequestReconciled)
	if !ok {
		t.Fatalf("outcome = %T", creation.Outcome)
	}
	if _, ok := reconciled.ChangeRequest.State.(delivery.DraftState); !ok {
		t.Fatalf("state = %T", reconciled.ChangeRequest.State)
	}
	assertNoExternalMutation(t, runner.calls)
}

func contractRetryPush(t *testing.T) {
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stdout: []byte(newCommit + "\n")},
	)
	publication, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := publication.Outcome.(delivery.BranchAlreadyPublished); !ok {
		t.Fatalf("outcome = %T", publication.Outcome)
	}
	assertNoExternalMutation(t, runner.calls)
}

func contractRetryCreate(t *testing.T) { contractOwnedPullRequest(t) }

func contractRetryUpdate(t *testing.T) {
	request := finalChangeRequest()
	section := delivery.OwnedSection{Revision: creationContent(request).Revision, Body: renderFinalChangeRequestContent(creationContent(request))}
	body := renderOwnedSection(request.Owner, section)
	runner := scriptedRunner(t, commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: body}, newCommit)})
	update, err := operationAdapter(t, runner).UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
		OperationID: "retry-update", Ref: changeRequestRef(request, "42"), Owner: request.Owner,
		Intent: delivery.UpdateOwnedChangeRequest{Title: delivery.KeepTitle{}, OwnedSection: section},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := update.Outcome.(delivery.ChangeRequestUnchanged); !ok {
		t.Fatalf("outcome = %T", update.Outcome)
	}
	assertNoExternalMutation(t, runner.calls)
}

func contractRetryReady(t *testing.T) {
	request := incrementalDraftChangeRequest()
	content := request.Intent.(delivery.CreateIncrementalDraft).Content
	body := renderFinalChangeRequestBody(request.Owner, content)
	runner := scriptedRunner(t, commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body}, newCommit)})
	update, err := operationAdapter(t, runner).UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
		OperationID: "retry-ready", Ref: changeRequestRef(request, "52"), Owner: request.Owner,
		Intent: delivery.FinalizeIncrementalDraft{Title: delivery.KeepTitle{}, Content: content, Authorization: delivery.FinalValidationAuthorization{ValidatedHeadSHA: newCommit}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := update.Outcome.(delivery.ChangeRequestUnchanged); !ok {
		t.Fatalf("outcome = %T", update.Outcome)
	}
	assertNoExternalMutation(t, runner.calls)
}

func contractFinalCreate(t *testing.T) {
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	runner := createRunner(t, request, body, false)
	creation, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	created := creation.Outcome.(delivery.ChangeRequestCreated)
	if _, ok := created.ChangeRequest.State.(delivery.OpenState); !ok {
		t.Fatalf("state = %T", created.ChangeRequest.State)
	}
	assertCreateDraftValue(t, runner.calls, false)
	assertMutationCount(t, runner.calls, mutationPOST, 1)
}

func contractDraftCreate(t *testing.T) {
	request := incrementalDraftChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, request.Intent.(delivery.CreateIncrementalDraft).Content)
	runner := createRunner(t, request, body, true)
	creation, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	created := creation.Outcome.(delivery.ChangeRequestCreated)
	if _, ok := created.ChangeRequest.State.(delivery.DraftState); !ok {
		t.Fatalf("state = %T", created.ChangeRequest.State)
	}
	assertCreateDraftValue(t, runner.calls, true)
	assertMutationCount(t, runner.calls, mutationPOST, 1)
}

func contractDraftUpdate(t *testing.T) {
	request := incrementalDraftChangeRequest()
	content := request.Intent.(delivery.CreateIncrementalDraft).Content
	body := renderFinalChangeRequestBody(request.Owner, content)
	next := content
	next.Revision = "accepted-point-2"
	next.PointChecklist = append(next.PointChecklist, delivery.AcceptedPoint{ID: "DS-155", Summary: "Update the incremental draft."})
	after := renderFinalChangeRequestBody(request.Owner, next)
	runner := scriptedRunner(t,
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body, draft: true}, newCommit)},
		commandResponse{},
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: after, draft: true}, newCommit)},
	)
	update, err := operationAdapter(t, runner).UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
		OperationID: "draft-update", Ref: changeRequestRef(request, "52"), Owner: request.Owner,
		Intent: delivery.UpdateIncrementalDraft{Title: delivery.KeepTitle{}, Content: next, Authorization: delivery.PointAcceptanceAuthorization{AcceptedHeadSHA: newCommit, PointID: "DS-155", PointRevision: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := update.Outcome.(delivery.ChangeRequestUpdated); !ok {
		t.Fatalf("outcome = %T", update.Outcome)
	}
	assertMutationCount(t, runner.calls, mutationPATCH, 1)
	assertMutationCount(t, runner.calls, mutationReady, 0)
}

func contractDraftReady(t *testing.T) {
	request := incrementalDraftChangeRequest()
	content := request.Intent.(delivery.CreateIncrementalDraft).Content
	body := renderFinalChangeRequestBody(request.Owner, content)
	runner := scriptedRunner(t,
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body, draft: true}, newCommit)},
		commandResponse{},
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body}, newCommit)},
	)
	update, err := operationAdapter(t, runner).UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
		OperationID: "draft-ready", Ref: changeRequestRef(request, "52"), Owner: request.Owner,
		Intent: delivery.FinalizeIncrementalDraft{Title: delivery.KeepTitle{}, Content: content, Authorization: delivery.FinalValidationAuthorization{ValidatedHeadSHA: newCommit}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := update.Outcome.(delivery.ChangeRequestUpdated); !ok {
		t.Fatalf("outcome = %T", update.Outcome)
	}
	assertMutationCount(t, runner.calls, mutationPATCH, 0)
	assertMutationCount(t, runner.calls, mutationReady, 1)
}

func contractChecksSuccess(t *testing.T) {
	found := observeContractPullRequest(t,
		[]map[string]any{{"name": "test", "state": "SUCCESS", "bucket": "pass"}}, nil,
		map[string]any{"reviewDecision": "", "reviews": []any{}},
	)
	if _, ok := found.Checks.(delivery.RequiredChecksSuccessful); !ok {
		t.Fatalf("checks = %T", found.Checks)
	}
}

func contractChecksPending(t *testing.T) {
	found := observeContractPullRequest(t,
		[]map[string]any{{"name": "integration", "state": "IN_PROGRESS", "description": "running", "link": "https://example.test/check/1", "bucket": "pending"}}, errors.New("pending"),
		map[string]any{"reviewDecision": "", "reviews": []any{}},
	)
	pending, ok := found.Checks.(delivery.RequiredChecksPending)
	if !ok || len(pending.Checks) != 1 || pending.Checks[0].Name != "integration" {
		t.Fatalf("checks = %#v", found.Checks)
	}
}

func contractChecksFailure(t *testing.T) {
	found := observeContractPullRequest(t,
		[]map[string]any{{"name": "lint", "state": "FAILURE", "description": "failed", "link": "https://example.test/check/2", "bucket": "fail"}}, errors.New("failed"),
		map[string]any{"reviewDecision": "", "reviews": []any{}},
	)
	failed, ok := found.Checks.(delivery.RequiredChecksFailed)
	if !ok || len(failed.Checks) != 1 || failed.Checks[0].Name != "lint" {
		t.Fatalf("checks = %#v", found.Checks)
	}
}

func contractChecksNotConfigured(t *testing.T) {
	found := observeContractPullRequest(t, []map[string]any{}, nil, map[string]any{"reviewDecision": "", "reviews": []any{}})
	if _, ok := found.Checks.(delivery.RequiredChecksNotConfigured); !ok {
		t.Fatalf("checks = %T", found.Checks)
	}
}

func contractReviewChangesRequested(t *testing.T) {
	found := observeContractPullRequest(t,
		[]map[string]any{}, nil,
		map[string]any{"reviewDecision": "CHANGES_REQUESTED", "reviews": []any{
			map[string]any{"author": map[string]any{"login": "reviewer"}, "body": "Fix retry handling.", "state": "CHANGES_REQUESTED", "url": "https://example.test/review/1", "submittedAt": "2026-09-03T07:00:00Z"},
		}},
	)
	changes, ok := found.Review.(delivery.ReviewChangesRequested)
	if !ok || len(changes.Reviews) != 1 || changes.Reviews[0].Reviewer != "reviewer" {
		t.Fatalf("review = %#v", found.Review)
	}
}

func contractNoDuplicateMutation(t *testing.T) {
	contractRetryPush(t)
	contractRetryCreate(t)
	contractRetryUpdate(t)
	contractRetryReady(t)
}

func contractNoUnsafeForceRefspec(t *testing.T) {
	runner := publishRunnerForMissingBranch(t, commandResponse{})
	if _, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{})); err != nil {
		t.Fatal(err)
	}
	assertOneSafePush(t, runner.calls)
}

func createRunner(t *testing.T, request delivery.CreateChangeRequestRequest, body string, draft bool) *fakeRunner {
	t.Helper()
	return scriptedRunner(t,
		commandResponse{stdout: []byte(`[]`)},
		commandResponse{stdout: []byte(oldCommit + "\n")},
		commandResponse{stdout: []byte(newCommit + "\n")},
		commandResponse{stdout: []byte(`{"number":94}`)},
		commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 94, title: request.Title, body: body, draft: draft})},
	)
}

func publishRunnerForMissingBranch(t *testing.T, push commandResponse) *fakeRunner {
	t.Helper()
	return scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("not found")},
		push,
	)
}

func observeContractPullRequest(t *testing.T, checks any, checkErr error, review any) delivery.ChangeRequestFound {
	t.Helper()
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	checksJSON, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	reviewJSON, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	runner := scriptedRunner(t,
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: body}, newCommit)},
		commandResponse{stdout: checksJSON, err: checkErr},
		commandResponse{stdout: reviewJSON},
	)
	observation, err := operationAdapter(t, runner).ObserveChangeRequest(context.Background(), delivery.ObserveChangeRequestRequest{Ref: changeRequestRef(request, "42"), Owner: request.Owner})
	if err != nil {
		t.Fatal(err)
	}
	found, ok := observation.Outcome.(delivery.ChangeRequestFound)
	if !ok {
		t.Fatalf("outcome = %T", observation.Outcome)
	}
	assertNoExternalMutation(t, runner.calls)
	return found
}

type externalMutation string

const (
	mutationPush  externalMutation = "push"
	mutationPOST  externalMutation = "POST"
	mutationPATCH externalMutation = "PATCH"
	mutationReady externalMutation = "ready"
)

func mutationKind(call commandCall) externalMutation {
	if len(call.arguments) > 2 && call.arguments[2] == "push" {
		return mutationPush
	}
	if containsArgumentPair(call.arguments, "--method", "POST") {
		return mutationPOST
	}
	if containsArgumentPair(call.arguments, "--method", "PATCH") {
		return mutationPATCH
	}
	if len(call.arguments) > 1 && reflect.DeepEqual(call.arguments[:2], []string{"pr", "ready"}) {
		return mutationReady
	}
	return ""
}

func assertNoExternalMutation(t *testing.T, calls []commandCall) {
	t.Helper()
	for _, call := range calls {
		if kind := mutationKind(call); kind != "" {
			t.Fatalf("unexpected %s mutation: %#v", kind, call.arguments)
		}
	}
}

func assertMutationCount(t *testing.T, calls []commandCall, kind externalMutation, want int) {
	t.Helper()
	got := 0
	for _, call := range calls {
		if mutationKind(call) == kind {
			got++
		}
	}
	if got != want {
		t.Fatalf("%s mutation count = %d, want %d; calls = %#v", kind, got, want, calls)
	}
}

func assertOneSafePush(t *testing.T, calls []commandCall) {
	t.Helper()
	assertMutationCount(t, calls, mutationPush, 1)
	for _, call := range calls {
		if mutationKind(call) != mutationPush {
			continue
		}
		assertNoUnsafeForce(t, call.arguments)
		leaseCount := 0
		for _, argument := range call.arguments {
			if strings.HasPrefix(argument, "--force-with-lease=refs/heads/") {
				leaseCount++
			}
			if strings.HasPrefix(argument, "+") {
				t.Fatalf("unsafe forced refspec = %q", argument)
			}
		}
		if leaseCount != 1 {
			t.Fatalf("exact lease count = %d, want 1: %#v", leaseCount, call.arguments)
		}
	}
}

func assertCreateDraftValue(t *testing.T, calls []commandCall, want bool) {
	t.Helper()
	for _, call := range calls {
		if mutationKind(call) != mutationPOST {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(call.input, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["draft"] != want {
			t.Fatalf("draft = %#v, want %v", payload["draft"], want)
		}
		return
	}
	t.Fatal("create mutation not found")
}
