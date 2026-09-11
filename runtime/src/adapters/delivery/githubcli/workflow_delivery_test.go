package githubcli

import (
	"context"
	"darkstar/src/ports/delivery"
	"encoding/json"
	"errors"
	"testing"
)

func TestWorkflowPushNeedsNoPointOrValidationEvidence(t *testing.T) {
	request := publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{})
	request.Timing = delivery.PublishWorkflowCommit{CommitSHA: newCommit}
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("exit")},
		commandResponse{},
	)
	result, err := operationAdapter(t, runner).PublishBranch(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outcome.(delivery.BranchPublished); !ok || result.CommitSHA != newCommit {
		t.Fatalf("publication: %#v", result)
	}
	assertNoUnsafeForce(t, runner.calls[3].arguments)
}

func TestWorkflowPRCreationAndRetryWithoutValidationPolicy(t *testing.T) {
	request := finalChangeRequest()
	request.Intent = delivery.CreateWorkflowChangeRequest{HeadSHA: newCommit, Body: "## Changes\n\nImplement the requested change.\n\n## Tests\n\nNone requested.", Draft: false}
	body := renderOwnedSection(request.Owner, delivery.OwnedSection{Revision: request.OperationID, Body: "## Changes\n\nImplement the requested change.\n\n## Tests\n\nNone requested."})
	response := pullResponsesJSON(t, pullSpec{number: 42, title: request.Title, body: body})
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte(`[]`)},
		commandResponse{stdout: []byte(oldCommit + "\n")},
		commandResponse{stdout: []byte(newCommit + "\n")},
		commandResponse{stdout: []byte(`{"number":42}`)},
		commandResponse{stdout: response},
		commandResponse{stdout: response},
	)
	adapter := operationAdapter(t, runner)
	result, err := adapter.CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outcome.(delivery.ChangeRequestCreated); !ok {
		t.Fatalf("creation outcome: %T", result.Outcome)
	}
	var payload createPullRequestPayload
	if err = json.Unmarshal(runner.calls[3].input, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Body != body || payload.Draft {
		t.Fatalf("wrong PR content: %#v", payload)
	}
	result, err = adapter.CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outcome.(delivery.ChangeRequestReconciled); !ok || len(runner.calls) != 6 {
		t.Fatal("retry did not reconcile the existing PR")
	}
}
