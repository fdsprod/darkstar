package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/readinesscontrol"
	"darkstar/src/core/routeassessment"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

const (
	readinessRunID        = "run_01K3Z1C2AAAAAAAAAAAAAAAAAA"
	readinessAssessmentID = "assessment_01K3Z1C2AAAAAAAAAAAAAAAAAA"
)

func TestRunReadinessShowUsesLatestQueryAndReadableOutput(t *testing.T) {
	client := &recordingReadinessClient{current: readinessViewFixture()}
	var stdout, stderr bytes.Buffer
	code := executeRunReadiness(context.Background(), client, readinessCommand{
		operation: "show", runID: readinessRunID,
	}, false, &stdout, &stderr)

	if code != int(ExitSuccess) || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(client.calls) != 1 || client.calls[0].method != http.MethodGet || client.calls[0].resource != "runs/"+readinessRunID+"/readiness" || client.calls[0].body != nil {
		t.Fatalf("calls = %#v", client.calls)
	}
	for _, expected := range []string{
		"Readiness " + readinessAssessmentID + " for " + readinessRunID + ": choice_required (pending).",
		"Node: review",
		"[information] context: Useful context is available.",
		"[recommendation] missing_input: Supply the request.",
		"[policy_gate] owner_review: Owner approval is missing.",
		"[invariant] repository: A repository is required.",
		"Route change: patch_1 (require_approval)",
		"Impact: +1/-0 nodes, +1/-0 transitions",
		"- continue",
		"- supply_input --remedy supply_request (Attach the requested input.)",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunReadinessDecideGetsLatestThenPostsExactDigestVersionAndHeaders(t *testing.T) {
	current := readinessViewFixture()
	decided := current
	decided.Status = statestore.ReadinessAssessmentDecided
	decided.ResourceVersion++
	decided.AllowedActions = []readinesscontrol.AllowedAction{}
	decided.Decision = &statestore.ReadinessDecisionProjection{
		DecisionID: "decision_1", Choice: string(routeassessment.ChoiceSupplyInput), RemedyCode: "supply_request",
		Reason: "I will attach the source request.", EffectStatus: statestore.ReadinessEffectPending,
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, DecidedAt: time.Date(2026, 9, 3, 12, 30, 0, 0, time.UTC),
	}
	client := &recordingReadinessClient{current: current, decided: decided}
	var stdout, stderr bytes.Buffer
	code := executeRunReadiness(context.Background(), client, readinessCommand{
		operation: "decide", runID: readinessRunID, action: routeassessment.ChoiceSupplyInput,
		reason: "I will attach the source request.", remedyCode: "supply_request", idempotencyKey: "readiness-command-key",
	}, true, &stdout, &stderr)

	if code != int(ExitSuccess) || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(client.calls) != 2 {
		t.Fatalf("calls = %#v", client.calls)
	}
	get, post := client.calls[0], client.calls[1]
	if get.method != http.MethodGet || get.resource != "runs/"+readinessRunID+"/readiness" || get.body != nil {
		t.Fatalf("GET = %#v", get)
	}
	if post.method != http.MethodPost || post.resource != "runs/"+readinessRunID+"/readiness/decisions" {
		t.Fatalf("POST = %#v", post)
	}
	wantBody := readinessDecisionBody{
		Action: routeassessment.ChoiceSupplyInput, AssessmentDigest: strings.Repeat("a", 64),
		Reason: "I will attach the source request.", RemedyCode: "supply_request",
	}
	if !reflect.DeepEqual(post.body, wantBody) {
		t.Fatalf("body = %#v, want %#v", post.body, wantBody)
	}
	if post.headers.Get("Idempotency-Key") != "readiness-command-key" || post.headers.Get("If-Match") != `"3"` {
		t.Fatalf("headers = %#v", post.headers)
	}
	encodedBody, err := json.Marshal(post.body)
	if err != nil {
		t.Fatal(err)
	}
	if string(encodedBody) != `{"action":"supply_input","assessmentDigest":"`+strings.Repeat("a", 64)+`","reason":"I will attach the source request.","remedyCode":"supply_request"}` {
		t.Fatalf("encoded body = %s", encodedBody)
	}
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
		Result        struct {
			Status   string `json:"status"`
			Decision struct {
				EffectStatus string `json:"effectStatus"`
			} `json:"decision"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.SchemaVersion != 1 || envelope.Result.Status != "decided" || envelope.Result.Decision.EffectStatus != "pending" {
		t.Fatalf("output = %s error=%v", stdout.String(), err)
	}
}

func TestParseRunReadinessValidatesClosedDecisionShape(t *testing.T) {
	valid, err := parseRunReadiness([]string{
		"decide", readinessRunID, "--reason", "Proceed with the reviewed route.",
		"--action", "continue", "--idempotency-key", "stable-key",
	})
	if err != nil || valid.action != routeassessment.ChoiceContinue || valid.reason != "Proceed with the reviewed route." || valid.idempotencyKey != "stable-key" {
		t.Fatalf("valid = %#v, %v", valid, err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown operation", []string{"inspect", readinessRunID}, "show|decide"},
		{"invalid run", []string{"show", "run_bad"}, "canonical run_ ULID"},
		{"show options", []string{"show", readinessRunID, "--action", "continue"}, "expected run readiness show"},
		{"missing action", []string{"decide", readinessRunID, "--reason", "Because"}, "--action must be one of"},
		{"unknown action", []string{"decide", readinessRunID, "--action", "add", "--reason", "Because"}, "--action must be one of"},
		{"missing reason", []string{"decide", readinessRunID, "--action", "continue"}, "--reason is required"},
		{"trimmed reason", []string{"decide", readinessRunID, "--action", "continue", "--reason", " Because "}, "without surrounding whitespace"},
		{"supply without remedy", []string{"decide", readinessRunID, "--action", "supply_input", "--reason", "Because"}, "supply_input requires --remedy"},
		{"remedy on continue", []string{"decide", readinessRunID, "--action", "continue", "--reason", "Because", "--remedy", "supply_request"}, "--remedy is not valid for continue"},
		{"short key", []string{"decide", readinessRunID, "--action", "cancel", "--reason", "Because", "--idempotency-key", "short"}, "between 8 and 128 bytes"},
		{"unknown option", []string{"decide", readinessRunID, "--action", "cancel", "--reason", "Because", "--force", "true"}, "unknown run readiness decide option"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRunReadiness(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunReadinessDecisionRefusesIncompleteConcurrencyBoundary(t *testing.T) {
	view := readinessViewFixture()
	view.ResourceVersion = 0
	client := &recordingReadinessClient{current: view}
	var stdout, stderr bytes.Buffer
	code := executeRunReadiness(context.Background(), client, readinessCommand{
		operation: "decide", runID: readinessRunID, action: routeassessment.ChoiceCancel,
		reason: "Stop this assessment.", idempotencyKey: "stable-key",
	}, false, &stdout, &stderr)
	if code != int(ExitInvariantViolation) || len(client.calls) != 1 || !strings.Contains(stderr.String(), "incomplete readiness concurrency boundary") {
		t.Fatalf("code=%d calls=%#v stderr=%q", code, client.calls, stderr.String())
	}
}

type readinessClientCall struct {
	method   string
	resource string
	body     any
	headers  http.Header
}

type recordingReadinessClient struct {
	current readinesscontrol.View
	decided readinesscontrol.View
	calls   []readinessClientCall
}

func (client *recordingReadinessClient) DoJSON(_ context.Context, method, resource string, body, response any, options ...clientapi.RequestOption) error {
	request, _ := http.NewRequest(method, "http://localhost/", nil)
	for _, option := range options {
		option(request)
	}
	client.calls = append(client.calls, readinessClientCall{method: method, resource: resource, body: body, headers: request.Header.Clone()})
	destination, ok := response.(*readinessViewResponse)
	if !ok {
		return nil
	}
	if method == http.MethodGet {
		*destination = readinessViewResponse(client.current)
	} else {
		*destination = readinessViewResponse(client.decided)
	}
	return nil
}

func TestReadinessViewResponseDecodesEveryFindingVariant(t *testing.T) {
	encoded, err := json.Marshal(readinessViewFixture())
	if err != nil {
		t.Fatal(err)
	}
	var response readinessViewResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	findings := readinesscontrol.View(response).Assessment.Findings
	if len(findings) != 4 {
		t.Fatalf("findings = %#v", findings)
	}
	want := []routeassessment.Level{
		routeassessment.LevelInformation,
		routeassessment.LevelRecommendation,
		routeassessment.LevelPolicyGate,
		routeassessment.LevelInvariant,
	}
	for index, finding := range findings {
		if finding.Level() != want[index] {
			t.Fatalf("finding %d level = %q, want %q", index, finding.Level(), want[index])
		}
	}
}

func readinessViewFixture() readinesscontrol.View {
	remedy := workflow.ReadinessRemedy{
		Code: "supply_request", Target: "review", Action: workflow.ReadinessSupplyInput,
		Description: "Attach the requested input.",
	}
	return readinesscontrol.View{
		Assessment: routeassessment.View{
			AssessmentID: readinessAssessmentID, RunID: readinessRunID, NodeID: "review",
			Scores: []routeassessment.Score{{Name: "completeness", Value: 0.5, Evidence: []routeassessment.Evidence{{Source: "request", Observation: "Scope is partial."}}}},
			Findings: []routeassessment.Finding{
				routeassessment.InformationFinding{Code: "context", Summary: "Useful context is available.", Evidence: []routeassessment.Evidence{{Source: "request", Observation: "A goal is present."}}},
				routeassessment.RecommendationFinding{Code: "missing_input", Summary: "Supply the request.", RemedyCode: "supply_request", Evidence: []routeassessment.Evidence{{Source: "request", Observation: "Details are absent."}}},
				routeassessment.PolicyGateFinding{Code: "owner_review", Summary: "Owner approval is missing.", Policy: "owner_review", Status: routeassessment.GateUnsatisfied, RemedyCode: "supply_request", Evidence: []routeassessment.Evidence{{Source: "policy", Observation: "No approval."}}},
				routeassessment.InvariantFinding{Code: "repository", Summary: "A repository is required.", Invariant: "repository", Status: routeassessment.InvariantViolated, RemedyCode: "supply_request", Evidence: []routeassessment.Evidence{{Source: "workspace", Observation: "Repository unavailable."}}},
			},
			Remedies: []workflow.ReadinessRemedy{remedy}, Disposition: routeassessment.DispositionChoiceRequired,
			Digest: strings.Repeat("a", 64),
			RouteChange: &routeassessment.RouteChangeView{
				PatchID: "patch_1", AuthorizationMode: workflow.RoutePatchRequireApproval,
				Impact: workflow.RoutePatchImpact{AddedNodes: []workflow.Identifier{"clarify"}, AddedTransitions: []workflow.Identifier{"clarify_review"}},
			},
		},
		Status: statestore.ReadinessAssessmentPending,
		AllowedActions: []readinesscontrol.AllowedAction{
			{Choice: routeassessment.ChoiceContinue},
			{Choice: routeassessment.ChoiceSupplyInput, Remedy: &remedy},
		},
		ResourceVersion: 3,
		CreatedAt:       time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 9, 3, 12, 15, 0, 0, time.UTC),
	}
}
