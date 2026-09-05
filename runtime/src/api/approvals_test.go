package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	checkpoint "darkstar/src/core/artifactcheckpoint"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
)

func TestApprovalDecisionAPIRequiresRevisionAndPassesExactScope(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingApprovalService{}
	if err := server.SetApprovals(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	approvalID := "approval_00000000000000000000000000"
	body := `{"action":"request_changes","scopeDigest":"scope","policyDigest":"policy","comment":"add recovery"}`

	missingMatch := approvalRequest(t, endpoint, approvalID, body, "decision-key", "")
	assertAPIError(t, missingMatch, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = missingMatch.Body.Close()

	response := approvalRequest(t, endpoint, approvalID, body, "decision-key", `"7"`)
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") != `"8"` {
		t.Fatalf("status = %d, ETag = %q", response.StatusCode, response.Header.Get("ETag"))
	}
	_ = response.Body.Close()
	if service.request.ApprovalID != approvalID || service.request.ExpectedResourceVersion != 7 ||
		service.request.Action != checkpointport.ActionRequestChanges || service.request.ScopeDigest != "scope" ||
		service.request.PolicyDigest != "policy" || service.request.Comment != "add recovery" ||
		service.request.IdempotencyKey != "decision-key" || service.request.Actor.ID != "local-user" {
		t.Fatalf("decision request = %#v", service.request)
	}
}

func TestApprovalDecisionAPIMapsResolvedAndIdempotencyConflicts(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingApprovalService{err: checkpointport.ErrAlreadyResolved}
	if err := server.SetApprovals(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	body := `{"action":"approve","scopeDigest":"scope","policyDigest":"policy"}`

	response := approvalRequest(t, endpoint, "approval_00000000000000000000000000", body, "decision-key", `"1"`)
	assertAPIError(t, response, http.StatusConflict, "APPROVAL_ALREADY_RESOLVED")
	_ = response.Body.Close()
	service.err = checkpointport.ErrIdempotencyConflict
	response = approvalRequest(t, endpoint, "approval_00000000000000000000000000", body, "decision-key", `"1"`)
	assertAPIError(t, response, http.StatusConflict, "APPROVAL_IDEMPOTENCY_CONFLICT")
	_ = response.Body.Close()
	service.err = checkpoint.ErrInvalidRequest
	response = approvalRequest(t, endpoint, "approval_00000000000000000000000000", body, "decision-key", `"1"`)
	assertAPIError(t, response, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = response.Body.Close()
}

func TestCheckpointQueueIsDiscoverableWithoutKnownID(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingApprovalService{queue: checkpointport.Queue{SchemaVersion: 1, Items: []checkpointport.Round{{ApprovalID: "approval_00000000000000000000000000", AllowedActions: []checkpointport.Action{checkpointport.ActionApprove}}}}}
	if err := server.SetApprovals(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL()+"/api/v1/checkpoints?class=workflow_checkpoint&status=pending", nil)
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var queue checkpointport.Queue
	if err := json.NewDecoder(response.Body).Decode(&queue); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || service.listRequest.Status != "pending" || len(queue.Items) != 1 || len(queue.Items[0].AllowedActions) != 1 {
		t.Fatalf("response=%d request=%#v queue=%#v", response.StatusCode, service.listRequest, queue)
	}
}

func TestReviewFeedbackAPIRequiresExactCandidateAndRevision(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingApprovalService{session: checkpointport.ReviewSession{SchemaVersion: 1, ID: "approval_00000000000000000000000000", ResourceVersion: 8, State: checkpointport.ReviewAwaitingAgent}}
	if err := server.SetApprovals(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	body := `{"candidateDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scopeDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","message":"cover reconnect"}`
	request, _ := http.NewRequest(http.MethodPost, endpoint.BaseURL()+"/api/v1/review-sessions/approval_00000000000000000000000000/feedback", bytes.NewBufferString(body))
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "feedback-key")
	request.Header.Set("If-Match", `"7"`)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") != `"8"` {
		t.Fatalf("status=%d etag=%q", response.StatusCode, response.Header.Get("ETag"))
	}
	if service.feedback.ExpectedResourceVersion != 7 || service.feedback.CandidateDigest != strings.Repeat("a", 64) ||
		service.feedback.ScopeDigest != strings.Repeat("b", 64) || service.feedback.Message != "cover reconnect" || service.feedback.IdempotencyKey != "feedback-key" {
		t.Fatalf("feedback request = %#v", service.feedback)
	}
}

type recordingApprovalService struct {
	request     checkpoint.DecisionRequest
	listRequest checkpoint.ListRequest
	queue       checkpointport.Queue
	err         error
	session     checkpointport.ReviewSession
	feedback    checkpoint.FeedbackRequest
}

func (service *recordingApprovalService) Decide(_ context.Context, request checkpoint.DecisionRequest) (checkpointport.Round, error) {
	service.request = request
	if service.err != nil {
		return checkpointport.Round{}, service.err
	}
	return checkpointport.Round{ApprovalID: request.ApprovalID, ResourceVersion: request.ExpectedResourceVersion + 1}, nil
}

func (service *recordingApprovalService) Round(context.Context, string) (checkpointport.Round, error) {
	return checkpointport.Round{}, service.err
}

func (service *recordingApprovalService) History(context.Context, string) (checkpointport.History, error) {
	return checkpointport.History{}, service.err
}

func (service *recordingApprovalService) List(_ context.Context, request checkpoint.ListRequest) (checkpointport.Queue, error) {
	service.listRequest = request
	if service.queue.Items == nil {
		service.queue = checkpointport.Queue{SchemaVersion: 1, Items: []checkpointport.Round{}}
	}
	return service.queue, service.err
}

func (service *recordingApprovalService) ReviewSession(context.Context, string) (checkpointport.ReviewSession, error) {
	return service.session, service.err
}
func (service *recordingApprovalService) ReviewHistory(context.Context, string) (checkpointport.ReviewHistory, error) {
	return checkpointport.ReviewHistory{}, service.err
}
func (service *recordingApprovalService) SubmitFeedback(_ context.Context, request checkpoint.FeedbackRequest) (checkpointport.ReviewSession, error) {
	service.feedback = request
	return service.session, service.err
}
func (service *recordingApprovalService) ResumeRevision(context.Context, checkpoint.ResumeRequest) (checkpointport.ReviewSession, error) {
	return service.session, service.err
}
func (service *recordingApprovalService) RecordAgentResponse(context.Context, checkpoint.AgentResponseRequest) (checkpointport.ReviewSession, error) {
	return service.session, service.err
}

func approvalRequest(t *testing.T, endpoint Endpoint, approvalID, body, key, ifMatch string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL()+"/api/v1/approvals/"+approvalID+"/decisions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
