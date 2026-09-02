package api

import (
	"bytes"
	"context"
	"net/http"
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

type recordingApprovalService struct {
	request checkpoint.DecisionRequest
	err     error
}

func (service *recordingApprovalService) Decide(_ context.Context, request checkpoint.DecisionRequest) (checkpointport.Round, error) {
	service.request = request
	if service.err != nil {
		return checkpointport.Round{}, service.err
	}
	return checkpointport.Round{ApprovalID: request.ApprovalID, ResourceVersion: request.ExpectedResourceVersion + 1}, nil
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
