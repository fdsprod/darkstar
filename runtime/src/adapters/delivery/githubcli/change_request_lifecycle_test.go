package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

func TestUpdateChangeRequestPreservesHumanTextOutsideOwnedSection(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	content := creationContent(request)
	beforeBody := "Human introduction.\n\n" + renderFinalChangeRequestBody(request.Owner, content) + "\n\nHuman follow-up.\n"
	content.Revision = "accepted-point-2"
	content.PointChecklist = append(content.PointChecklist, delivery.AcceptedPoint{ID: "DS-154", Summary: "Preserve human-authored text."})
	desiredOwned := delivery.OwnedSection{Revision: content.Revision, Body: renderFinalChangeRequestContent(content)}
	afterBody := "Human introduction.\n\n" + renderOwnedSection(request.Owner, desiredOwned) + "\n\nHuman follow-up.\n"
	ref := changeRequestRef(request, "42")
	runner := scriptedRunner(t,
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: beforeBody}, newCommit)},
		commandResponse{stdout: []byte(`{"number":42}`)},
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: afterBody}, newCommit)},
	)
	adapter := operationAdapter(t, runner)
	update, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
		OperationID: "update-pr-1", Ref: ref, Owner: request.Owner,
		Intent: delivery.UpdateOwnedChangeRequest{Title: delivery.KeepTitle{}, OwnedSection: desiredOwned},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, ok := update.Outcome.(delivery.ChangeRequestUpdated)
	if !ok || updated.ChangeRequest.Body != afterBody {
		t.Fatalf("outcome = %#v", update.Outcome)
	}
	var payload map[string]string
	if err := json.Unmarshal(runner.calls[1].input, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["body"] != afterBody || !strings.HasPrefix(payload["body"], "Human introduction.") || !strings.HasSuffix(payload["body"], "Human follow-up.\n") {
		t.Fatalf("updated body did not preserve human text: %q", payload["body"])
	}
	if !containsArgumentPair(runner.calls[1].arguments, "--method", "PATCH") {
		t.Fatalf("update arguments = %#v", runner.calls[1].arguments)
	}
}

func TestUpdateChangeRequestRejectsDuplicateOrMismatchedOwnershipMarkers(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	owned := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	otherOwner := delivery.ChangeRequestOwner{DeliveryLineID: "other-line", WorkItemID: "DAR-91"}
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: "Human-authored pull request."},
		{name: "duplicate", body: owned + "\n\n" + owned},
		{name: "mismatched owner", body: renderFinalChangeRequestBody(otherOwner, creationContent(request))},
		{name: "malformed", body: strings.Replace(owned, ownedSectionEnd, "<!-- /darkstar:owned-change-request:v1 malformed -->", 1)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := scriptedRunner(t, commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: test.body}, newCommit)})
			adapter := operationAdapter(t, runner)
			_, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
				OperationID: "update-pr-invalid", Ref: changeRequestRef(request, "42"), Owner: request.Owner,
				Intent: delivery.UpdateOwnedChangeRequest{Title: delivery.KeepTitle{}, OwnedSection: delivery.OwnedSection{Revision: "next", Body: "## Summary\n\nNext."}},
			})
			assertFailureCode(t, err, ports.FailureConflict)
			if len(runner.calls) != 1 {
				t.Fatalf("calls = %d, invalid ownership must fail before mutation", len(runner.calls))
			}
		})
	}
}

func TestUpdateChangeRequestNoOpAndUncertainPatchReconciliation(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	section := delivery.OwnedSection{Revision: creationContent(request).Revision, Body: renderFinalChangeRequestContent(creationContent(request))}
	body := renderOwnedSection(request.Owner, section)
	ref := changeRequestRef(request, "42")
	t.Run("no-op", func(t *testing.T) {
		runner := scriptedRunner(t, commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: body}, newCommit)})
		adapter := operationAdapter(t, runner)
		update, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
			OperationID: "update-pr-noop", Ref: ref, Owner: request.Owner,
			Intent: delivery.UpdateOwnedChangeRequest{Title: delivery.KeepTitle{}, OwnedSection: section},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := update.Outcome.(delivery.ChangeRequestUnchanged); !ok || len(runner.calls) != 1 {
			t.Fatalf("outcome/calls = %#v / %d", update.Outcome, len(runner.calls))
		}
	})
	t.Run("uncertain patch", func(t *testing.T) {
		next := delivery.OwnedSection{Revision: "next", Body: "## Summary\n\nUpdated evidence."}
		afterBody := renderOwnedSection(request.Owner, next)
		runner := scriptedRunner(t,
			commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: body}, newCommit)},
			commandResponse{err: errors.New("connection lost after PATCH")},
			commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 42, title: request.Title, body: afterBody}, newCommit)},
		)
		adapter := operationAdapter(t, runner)
		update, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
			OperationID: "update-pr-reconcile", Ref: ref, Owner: request.Owner,
			Intent: delivery.UpdateOwnedChangeRequest{Title: delivery.KeepTitle{}, OwnedSection: next},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := update.Outcome.(delivery.ChangeRequestUpdateReconciled); !ok {
			t.Fatalf("outcome = %T", update.Outcome)
		}
	})
}

func TestIncrementalDraftCreateAdoptRefreshAndReadyLifecycle(t *testing.T) {
	t.Parallel()
	draftRequest := incrementalDraftChangeRequest()
	content := draftRequest.Intent.(delivery.CreateIncrementalDraft).Content
	body := renderFinalChangeRequestBody(draftRequest.Owner, content)

	t.Run("create draft", func(t *testing.T) {
		runner := scriptedRunner(t,
			commandResponse{stdout: []byte(`[]`)},
			commandResponse{stdout: []byte(oldCommit + "\n")},
			commandResponse{stdout: []byte(newCommit + "\n")},
			commandResponse{stdout: []byte(`{"number":52}`)},
			commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 52, title: draftRequest.Title, body: body, draft: true})},
		)
		adapter := operationAdapter(t, runner)
		creation, err := adapter.CreateChangeRequest(context.Background(), draftRequest)
		if err != nil {
			t.Fatal(err)
		}
		created, ok := creation.Outcome.(delivery.ChangeRequestCreated)
		if !ok {
			t.Fatalf("outcome = %T", creation.Outcome)
		}
		if _, ok := created.ChangeRequest.State.(delivery.DraftState); !ok {
			t.Fatalf("state = %T", created.ChangeRequest.State)
		}
		var payload map[string]any
		if err := json.Unmarshal(runner.calls[3].input, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["draft"] != true {
			t.Fatalf("draft payload = %#v", payload["draft"])
		}
	})

	t.Run("adopt one draft", func(t *testing.T) {
		runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 52, title: draftRequest.Title, body: body, draft: true})})
		adapter := operationAdapter(t, runner)
		creation, err := adapter.CreateChangeRequest(context.Background(), draftRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := creation.Outcome.(delivery.ChangeRequestReconciled); !ok || len(runner.calls) != 1 {
			t.Fatalf("outcome/calls = %#v / %d", creation.Outcome, len(runner.calls))
		}
	})

	t.Run("refresh accepted point", func(t *testing.T) {
		nextContent := content
		nextContent.Revision = "accepted-point-2"
		nextContent.PointChecklist = append(nextContent.PointChecklist, delivery.AcceptedPoint{ID: "DS-155", Summary: "Refresh the same draft."})
		afterBody := renderFinalChangeRequestBody(draftRequest.Owner, nextContent)
		runner := scriptedRunner(t,
			commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: draftRequest.Title, body: body, draft: true}, newCommit)},
			commandResponse{},
			commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: draftRequest.Title, body: afterBody, draft: true}, newCommit)},
		)
		adapter := operationAdapter(t, runner)
		update, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
			OperationID: "draft-point-2", Ref: changeRequestRef(draftRequest, "52"), Owner: draftRequest.Owner,
			Intent: delivery.UpdateIncrementalDraft{
				Title: delivery.KeepTitle{}, Content: nextContent,
				Authorization: delivery.PointAcceptanceAuthorization{AcceptedHeadSHA: newCommit, PointID: "DS-155", PointRevision: 2},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := update.Outcome.(delivery.ChangeRequestUpdated); !ok {
			t.Fatalf("outcome = %T", update.Outcome)
		}
	})

	t.Run("ready exactly once", func(t *testing.T) {
		ref := changeRequestRef(draftRequest, "52")
		runner := scriptedRunner(t,
			commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: draftRequest.Title, body: body, draft: true}, newCommit)},
			commandResponse{},
			commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: draftRequest.Title, body: body}, newCommit)},
		)
		adapter := operationAdapter(t, runner)
		update, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
			OperationID: "draft-ready", Ref: ref, Owner: draftRequest.Owner,
			Intent: delivery.FinalizeIncrementalDraft{Title: delivery.KeepTitle{}, Content: content, Authorization: delivery.FinalValidationAuthorization{ValidatedHeadSHA: newCommit}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := update.Outcome.(delivery.ChangeRequestUpdated); !ok {
			t.Fatalf("outcome = %T", update.Outcome)
		}
		if !reflect.DeepEqual(runner.calls[1].arguments, []string{"pr", "ready", "52", "--repo", "darkstar/runtime"}) {
			t.Fatalf("ready arguments = %#v", runner.calls[1].arguments)
		}

		retryRunner := scriptedRunner(t, commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: draftRequest.Title, body: body}, newCommit)})
		retryAdapter := operationAdapter(t, retryRunner)
		retry, err := retryAdapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
			OperationID: "draft-ready", Ref: ref, Owner: draftRequest.Owner,
			Intent: delivery.FinalizeIncrementalDraft{Title: delivery.KeepTitle{}, Content: content, Authorization: delivery.FinalValidationAuthorization{ValidatedHeadSHA: newCommit}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := retry.Outcome.(delivery.ChangeRequestUnchanged); !ok || len(retryRunner.calls) != 1 {
			t.Fatalf("retry outcome/calls = %#v / %d", retry.Outcome, len(retryRunner.calls))
		}
	})
}

func TestObserveChangeRequestReportsClosedCheckAndReviewOutcomes(t *testing.T) {
	t.Parallel()
	request := incrementalDraftChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, request.Intent.(delivery.CreateIncrementalDraft).Content)
	ref := changeRequestRef(request, "52")
	tests := []struct {
		name       string
		checks     any
		checkError error
		review     any
		assert     func(*testing.T, delivery.ChangeRequestFound)
	}{
		{
			name:   "successful and approved",
			checks: []map[string]any{{"name": "test", "state": "SUCCESS", "description": "passed", "link": "https://example.test/checks/test", "bucket": "pass"}},
			review: map[string]any{"reviewDecision": "APPROVED", "reviews": []any{}},
			assert: func(t *testing.T, found delivery.ChangeRequestFound) {
				if _, ok := found.Checks.(delivery.RequiredChecksSuccessful); !ok {
					t.Fatalf("checks = %T", found.Checks)
				}
				if _, ok := found.Review.(delivery.ReviewApproved); !ok {
					t.Fatalf("review = %T", found.Review)
				}
			},
		},
		{
			name:       "pending",
			checks:     []map[string]any{{"name": "integration", "state": "IN_PROGRESS", "description": "running shard 2", "link": "https://example.test/checks/integration", "bucket": "pending"}},
			checkError: errors.New("checks pending"),
			review:     map[string]any{"reviewDecision": "REVIEW_REQUIRED", "reviews": []any{}},
			assert: func(t *testing.T, found delivery.ChangeRequestFound) {
				pending, ok := found.Checks.(delivery.RequiredChecksPending)
				if !ok || len(pending.Checks) != 1 || pending.Checks[0].Name != "integration" || pending.Checks[0].EvidenceRef == "" {
					t.Fatalf("checks = %#v", found.Checks)
				}
				if _, ok := found.Review.(delivery.ReviewPending); !ok {
					t.Fatalf("review = %T", found.Review)
				}
			},
		},
		{
			name:       "failed and changes requested",
			checks:     []map[string]any{{"name": "lint", "state": "FAILURE", "description": "format mismatch", "link": "https://example.test/checks/lint", "bucket": "fail"}},
			checkError: errors.New("checks failed"),
			review: map[string]any{"reviewDecision": "CHANGES_REQUESTED", "reviews": []any{
				map[string]any{"author": map[string]any{"login": "reviewer"}, "body": "Handle the retry race.", "state": "CHANGES_REQUESTED", "url": "https://example.test/reviews/7"},
			}},
			assert: func(t *testing.T, found delivery.ChangeRequestFound) {
				failed, ok := found.Checks.(delivery.RequiredChecksFailed)
				if !ok || len(failed.Checks) != 1 || failed.Checks[0].Summary != "format mismatch" {
					t.Fatalf("checks = %#v", found.Checks)
				}
				changes, ok := found.Review.(delivery.ReviewChangesRequested)
				if !ok || len(changes.Reviews) != 1 || changes.Reviews[0].Reviewer != "reviewer" || changes.Reviews[0].Summary != "Handle the retry race." {
					t.Fatalf("review = %#v", found.Review)
				}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			checks, _ := json.Marshal(test.checks)
			review, _ := json.Marshal(test.review)
			runner := scriptedRunner(t,
				commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body}, newCommit)},
				commandResponse{stdout: checks, err: test.checkError},
				commandResponse{stdout: review},
			)
			adapter := operationAdapter(t, runner)
			observation, err := adapter.ObserveChangeRequest(context.Background(), delivery.ObserveChangeRequestRequest{Ref: ref, Owner: request.Owner})
			if err != nil {
				t.Fatal(err)
			}
			found, ok := observation.Outcome.(delivery.ChangeRequestFound)
			if !ok {
				t.Fatalf("outcome = %T", observation.Outcome)
			}
			test.assert(t, found)
			if len(runner.calls) != 3 || !reflect.DeepEqual(runner.calls[1].arguments, []string{"pr", "checks", "52", "--repo", "darkstar/runtime", "--required", "--json", "name,state,description,link,bucket"}) {
				t.Fatalf("calls = %#v", runner.calls)
			}
		})
	}
}

func TestStructuredDraftContentMustMatchPointAndHeadAuthorizationBeforeCommands(t *testing.T) {
	t.Parallel()
	base := incrementalDraftChangeRequest()
	content := base.Intent.(delivery.CreateIncrementalDraft).Content
	tests := []struct {
		name   string
		invoke func(*Adapter) error
	}{
		{
			name: "create point mismatch",
			invoke: func(adapter *Adapter) error {
				request := base
				request.Intent = delivery.CreateIncrementalDraft{Content: content, Authorization: delivery.PointAcceptanceAuthorization{AcceptedHeadSHA: newCommit, PointID: "DS-other", PointRevision: 1}}
				_, err := adapter.CreateChangeRequest(context.Background(), request)
				return err
			},
		},
		{
			name: "create head mismatch",
			invoke: func(adapter *Adapter) error {
				request := base
				request.Intent = delivery.CreateIncrementalDraft{Content: content, Authorization: delivery.PointAcceptanceAuthorization{AcceptedHeadSHA: oldCommit, PointID: "DS-153", PointRevision: 1}}
				_, err := adapter.CreateChangeRequest(context.Background(), request)
				return err
			},
		},
		{
			name: "update point mismatch",
			invoke: func(adapter *Adapter) error {
				_, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
					OperationID: "draft-update-invalid", Ref: changeRequestRef(base, "52"), Owner: base.Owner,
					Intent: delivery.UpdateIncrementalDraft{Title: delivery.KeepTitle{}, Content: content, Authorization: delivery.PointAcceptanceAuthorization{AcceptedHeadSHA: newCommit, PointID: "DS-other", PointRevision: 2}},
				})
				return err
			},
		},
		{
			name: "finalize head mismatch",
			invoke: func(adapter *Adapter) error {
				_, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
					OperationID: "draft-finalize-invalid", Ref: changeRequestRef(base, "52"), Owner: base.Owner,
					Intent: delivery.FinalizeIncrementalDraft{Title: delivery.KeepTitle{}, Content: content, Authorization: delivery.FinalValidationAuthorization{ValidatedHeadSHA: oldCommit}},
				})
				return err
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := scriptedRunner(t)
			adapter := operationAdapter(t, runner)
			assertFailureCode(t, test.invoke(adapter), ports.FailureInvalidRequest)
			if len(runner.calls) != 0 {
				t.Fatalf("validation reached command boundary: %#v", runner.calls)
			}
		})
	}
}

func TestPullRequestCommandsUseEnterpriseHostInRepositorySelector(t *testing.T) {
	t.Parallel()
	request := incrementalDraftChangeRequest()
	repository := request.Coordinates.Base.Repository
	repository.Host = "ghe.example.test"
	request.Coordinates.Base.Repository = repository
	request.Coordinates.Head.Repository = repository
	content := request.Intent.(delivery.CreateIncrementalDraft).Content
	body := renderFinalChangeRequestBody(request.Owner, content)
	ref := changeRequestRef(request, "52")
	selector := "ghe.example.test/darkstar/runtime"

	observeRunner := scriptedRunner(t,
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body}, newCommit)},
		commandResponse{stdout: []byte(`[{"name":"test","state":"SUCCESS","bucket":"pass"}]`)},
		commandResponse{stdout: []byte(`{"reviewDecision":"","reviews":[]}`)},
	)
	observeAdapter := operationAdapter(t, observeRunner)
	if _, err := observeAdapter.ObserveChangeRequest(context.Background(), delivery.ObserveChangeRequestRequest{Ref: ref, Owner: request.Owner}); err != nil {
		t.Fatal(err)
	}
	if !containsArgumentPair(observeRunner.calls[1].arguments, "--repo", selector) || !containsArgumentPair(observeRunner.calls[2].arguments, "--repo", selector) {
		t.Fatalf("enterprise observe selectors = %#v / %#v", observeRunner.calls[1].arguments, observeRunner.calls[2].arguments)
	}

	readyRunner := scriptedRunner(t,
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body, draft: true}, newCommit)},
		commandResponse{},
		commandResponse{stdout: pullResponseJSON(t, pullSpec{number: 52, title: request.Title, body: body}, newCommit)},
	)
	readyAdapter := operationAdapter(t, readyRunner)
	_, err := readyAdapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{
		OperationID: "enterprise-ready", Ref: ref, Owner: request.Owner,
		Intent: delivery.FinalizeIncrementalDraft{Title: delivery.KeepTitle{}, Content: content, Authorization: delivery.FinalValidationAuthorization{ValidatedHeadSHA: newCommit}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgumentPair(readyRunner.calls[1].arguments, "--repo", selector) {
		t.Fatalf("enterprise ready selector = %#v", readyRunner.calls[1].arguments)
	}
}

func incrementalDraftChangeRequest() delivery.CreateChangeRequestRequest {
	request := finalChangeRequest()
	final := request.Intent.(delivery.CreateFinalChangeRequest)
	request.OperationID = "create-draft-1"
	request.Title = "Incremental delivery draft"
	request.Intent = delivery.CreateIncrementalDraft{
		Content: final.Content,
		Authorization: delivery.PointAcceptanceAuthorization{
			AcceptedHeadSHA: newCommit, PointID: "DS-153", PointRevision: 1,
		},
	}
	return request
}

func changeRequestRef(request delivery.CreateChangeRequestRequest, id string) delivery.ChangeRequestRef {
	return delivery.ChangeRequestRef{Repository: request.Coordinates.Base.Repository, ID: id}
}

func pullResponseJSON(t *testing.T, spec pullSpec, headSHA string) []byte {
	t.Helper()
	array := pullResponsesJSON(t, spec)
	var responses []map[string]any
	if err := json.Unmarshal(array, &responses); err != nil {
		t.Fatal(err)
	}
	head := responses[0]["head"].(map[string]any)
	head["sha"] = headSHA
	encoded, err := json.Marshal(responses[0])
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
