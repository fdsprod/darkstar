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

func TestCreateChangeRequestRendersFinalBodyAndCreatesNonDraft(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	wantBody := strings.Join([]string{
		ownerMarker(request.Owner),
		revisionMarker("validation-7"),
		"",
		"## Outcome",
		"",
		"Deliver one idempotent final pull request.",
		"",
		"## Scope",
		"",
		"- Add exact pull request reconciliation.",
		"",
		"## Artifacts",
		"",
		"- [Accepted design](https://example.test/artifacts/design)",
		"",
		"## Implementation points",
		"",
		"- [x] `DS-153` — Create the validated final PR.",
		"",
		"## Commits",
		"",
		"- `1111111111111111111111111111111111111111` — Define the delivery contract.",
		"- `2222222222222222222222222222222222222222` — Implement final PR creation.",
		"",
		"## Risk and rollback",
		"",
		"**Risk:** A retry could otherwise duplicate the pull request.",
		"",
		"**Rollback:** Close the owned PR and revert the adapter commit.",
		"",
		"## Validation evidence",
		"",
		"- [Go tests](https://example.test/evidence/tests) — Targeted and full suites passed.",
		"",
		"<!-- /darkstar:owned-change-request:v1 -->",
	}, "\n")
	if body != wantBody {
		t.Fatalf("body mismatch\n--- got ---\n%s\n--- want ---\n%s", body, wantBody)
	}

	runner := scriptedRunner(t,
		commandResponse{stdout: []byte(`[]`)},
		commandResponse{stdout: []byte(oldCommit + "\n")},
		commandResponse{stdout: []byte(newCommit + "\n")},
		commandResponse{stdout: []byte(`{"number":42}`)},
		commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 42, title: request.Title, body: body})},
	)
	adapter := operationAdapter(t, runner)
	creation, err := adapter.CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	created, ok := creation.Outcome.(delivery.ChangeRequestCreated)
	if !ok || created.ChangeRequest.Ref.ID != "42" || created.ChangeRequest.Body != body {
		t.Fatalf("outcome = %#v", creation.Outcome)
	}
	if _, ok := created.ChangeRequest.State.(delivery.OpenState); !ok {
		t.Fatalf("state = %T, want OpenState", created.ChangeRequest.State)
	}
	if creation.ObservedAt != observedAt || creation.EvidenceRef != "https://github.com/darkstar/runtime/pull/42" {
		t.Fatalf("creation evidence = %#v", creation)
	}

	if len(runner.calls) != 5 {
		t.Fatalf("calls = %d, want search, two exact ref reads, create, and reconciliation", len(runner.calls))
	}
	query := url.Values{"base": {"main"}, "head": {"darkstar:codex/dar-87"}, "per_page": {"100"}, "state": {"all"}}.Encode()
	wantSearch := []string{"api", "--hostname", "github.com", "repos/darkstar/runtime/pulls?" + query, "--method", "GET", "--paginate"}
	if !reflect.DeepEqual(runner.calls[0].arguments, wantSearch) || !reflect.DeepEqual(runner.calls[4].arguments, wantSearch) {
		t.Fatalf("search arguments = %#v / %#v", runner.calls[0].arguments, runner.calls[4].arguments)
	}
	wantCreate := []string{"api", "--hostname", "github.com", "repos/darkstar/runtime/pulls", "--method", "POST", "--input", "-"}
	if !reflect.DeepEqual(runner.calls[3].arguments, wantCreate) {
		t.Fatalf("create arguments = %#v", runner.calls[3].arguments)
	}
	var payload map[string]any
	if err := json.Unmarshal(runner.calls[3].input, &payload); err != nil {
		t.Fatal(err)
	}
	if draft, present := payload["draft"]; !present || draft != false {
		t.Fatalf("draft payload = %#v, present = %v", draft, present)
	}
	if payload["base"] != "main" || payload["head"] != "darkstar:codex/dar-87" || payload["head_repo"] != "runtime" || payload["body"] != body {
		t.Fatalf("create payload = %#v", payload)
	}
}

func TestCreateChangeRequestAdoptsExistingOwnedMatchWithoutMutation(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 19, title: request.Title, body: body})})
	adapter := operationAdapter(t, runner)
	creation, err := adapter.CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, ok := creation.Outcome.(delivery.ChangeRequestReconciled)
	if !ok || reconciled.ChangeRequest.Ref.ID != "19" {
		t.Fatalf("outcome = %#v", creation.Outcome)
	}
	if len(runner.calls) != 1 || !containsArgumentPair(runner.calls[0].arguments, "--method", "GET") {
		t.Fatalf("unexpected mutation calls = %#v", runner.calls)
	}
}

func TestCreateChangeRequestRejectsUnownedAndAmbiguousCollisions(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	tests := []struct {
		name  string
		pulls []pullSpec
	}{
		{name: "unowned", pulls: []pullSpec{{number: 7, title: request.Title, body: "Human-authored pull request."}}},
		{name: "ambiguous", pulls: []pullSpec{{number: 7, title: request.Title, body: body}, {number: 8, title: request.Title, body: body}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, test.pulls...)})
			adapter := operationAdapter(t, runner)
			_, err := adapter.CreateChangeRequest(context.Background(), request)
			assertFailureCode(t, err, ports.FailureConflict)
			if len(runner.calls) != 1 {
				t.Fatalf("calls = %d, collision must fail before mutation", len(runner.calls))
			}
		})
	}
}

func TestCreateChangeRequestRejectsDraftAndClosedOwnedCollisions(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	for _, test := range []struct {
		name string
		pull pullSpec
	}{
		{name: "draft", pull: pullSpec{number: 9, title: request.Title, body: body, draft: true}},
		{name: "closed without merge", pull: pullSpec{number: 10, title: request.Title, body: body, state: "closed"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, test.pull)})
			adapter := operationAdapter(t, runner)
			_, err := adapter.CreateChangeRequest(context.Background(), request)
			assertFailureCode(t, err, ports.FailureConflict)
			if len(runner.calls) != 1 {
				t.Fatalf("calls = %d, lifecycle collision must fail before mutation", len(runner.calls))
			}
		})
	}
}

func TestCreateChangeRequestReconcilesCreateCommandUncertainty(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte(`[]`)},
		commandResponse{stdout: []byte(oldCommit + "\n")},
		commandResponse{stdout: []byte(newCommit + "\n")},
		commandResponse{err: errors.New("connection lost after request")},
		commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 23, title: request.Title, body: body})},
	)
	adapter := operationAdapter(t, runner)
	creation, err := adapter.CreateChangeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, ok := creation.Outcome.(delivery.ChangeRequestReconciled)
	if !ok || reconciled.ChangeRequest.Ref.ID != "23" {
		t.Fatalf("outcome = %#v", creation.Outcome)
	}
	createCalls := 0
	for _, call := range runner.calls {
		if containsArgumentPair(call.arguments, "--method", "POST") {
			createCalls++
		}
	}
	if createCalls != 1 {
		t.Fatalf("create calls = %d, want exactly one", createCalls)
	}
}

func TestFindChangeRequestsFiltersToExactBaseAndHeadRepositoriesAndRefs(t *testing.T) {
	t.Parallel()
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t,
		pullSpec{number: 1, title: request.Title, body: body},
		pullSpec{number: 2, title: request.Title, body: body, headRepository: "darkstar/other"},
		pullSpec{number: 3, title: request.Title, body: body, headRef: "codex/other"},
	)})
	adapter := operationAdapter(t, runner)
	search, err := adapter.FindChangeRequests(context.Background(), delivery.FindChangeRequestsRequest{Coordinates: request.Coordinates, Owner: request.Owner})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Matches) != 1 || search.Matches[0].Ref.ID != "1" {
		t.Fatalf("matches = %#v", search.Matches)
	}
	owned, ok := search.Matches[0].Ownership.(delivery.OwnedChangeRequest)
	if !ok || owned.Owner != request.Owner || owned.Revision != creationContent(request).Revision {
		t.Fatalf("ownership = %#v", search.Matches[0].Ownership)
	}
}

func finalChangeRequest() delivery.CreateChangeRequestRequest {
	repository := testRepository()
	return delivery.CreateChangeRequestRequest{
		OperationID: "create-pr-1",
		Coordinates: delivery.ChangeRequestCoordinates{
			Base: delivery.BranchRef{Repository: repository, Name: "main"},
			Head: delivery.BranchRef{Repository: repository, Name: "codex/dar-87"},
		},
		Owner: delivery.ChangeRequestOwner{DeliveryLineID: "delivery-1", WorkItemID: "DAR-87"},
		Title: "Create the final pull request",
		Intent: delivery.CreateFinalChangeRequest{Content: delivery.FinalChangeRequestContent{
			Revision: "validation-7", Outcome: "Deliver one idempotent final pull request.",
			Scope:          []string{"Add exact pull request reconciliation."},
			ArtifactLinks:  []delivery.ArtifactLink{{Label: "Accepted design", URL: "https://example.test/artifacts/design"}},
			PointChecklist: []delivery.AcceptedPoint{{ID: "DS-153", Summary: "Create the validated final PR."}},
			Commits: []delivery.CommitSummary{
				{SHA: oldCommit, Summary: "Define the delivery contract."},
				{SHA: newCommit, Summary: "Implement final PR creation."},
			},
			RiskRollback: delivery.RiskRollback{Risk: "A retry could otherwise duplicate the pull request.", Rollback: "Close the owned PR and revert the adapter commit."},
			Evidence:     []delivery.EvidenceLink{{Label: "Go tests", URL: "https://example.test/evidence/tests", Summary: "Targeted and full suites passed."}},
		}, Authorization: delivery.FinalValidationAuthorization{ValidatedHeadSHA: newCommit}},
	}
}

func creationContent(request delivery.CreateChangeRequestRequest) delivery.FinalChangeRequestContent {
	return request.Intent.(delivery.CreateFinalChangeRequest).Content
}

type pullSpec struct {
	number         int
	title          string
	body           string
	draft          bool
	state          string
	baseRepository string
	baseRef        string
	headRepository string
	headRef        string
}

func pullResponsesJSON(t *testing.T, specs ...pullSpec) []byte {
	t.Helper()
	responses := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		baseRepository := spec.baseRepository
		if baseRepository == "" {
			baseRepository = "darkstar/runtime"
		}
		baseRef := spec.baseRef
		if baseRef == "" {
			baseRef = "main"
		}
		headRepository := spec.headRepository
		if headRepository == "" {
			headRepository = "darkstar/runtime"
		}
		headRef := spec.headRef
		if headRef == "" {
			headRef = "codex/dar-87"
		}
		state := spec.state
		if state == "" {
			state = "open"
		}
		responses = append(responses, map[string]any{
			"number": spec.number, "html_url": "https://github.com/darkstar/runtime/pull/" + jsonNumber(spec.number),
			"title": spec.title, "body": spec.body, "state": state, "draft": spec.draft, "merged_at": nil,
			"base": map[string]any{"ref": baseRef, "repo": map[string]any{"full_name": baseRepository}},
			"head": map[string]any{"ref": headRef, "repo": map[string]any{"full_name": headRepository}},
		})
	}
	encoded, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return strings.Trim(string(encoded), `"`)
}

func containsArgumentPair(arguments []string, first, second string) bool {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == first && arguments[index+1] == second {
			return true
		}
	}
	return false
}
