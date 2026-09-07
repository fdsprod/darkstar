package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"darkstar/src/core/attention"
)

func TestAttentionCheckpointAPIForwardsFiltersPaginationAndTypedItems(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingAttentionService{page: attention.Page{SchemaVersion: 1, Items: attention.Checkpoints{
		attention.WorkflowControl{Envelope: attention.Envelope{Kind: attention.KindWorkflowControl, ID: "approval_1", Context: attention.Context{ProjectID: "project_1", WorkItemID: "work_1", RunID: "run_1"}, Urgency: 90, CreatedAt: time.Now().UTC(), ResourceVersion: 2, Summary: "Control", DeepLink: "/checkpoints?itemId=approval_1"}, AllowedActions: []attention.WorkflowControlAction{attention.WorkflowControlApprove}},
	}, NextCursor: "next"}}
	if err := server.SetAttention(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL()+"/api/v1/attention?kind=workflow_control&kind=input_required&projectId=project_1&workItemId=work_1&runId=run_1&limit=7&cursor=resume", nil)
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var page attention.Page
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(page.Items) != 1 || page.NextCursor != "next" {
		t.Fatalf("response=%d page=%#v", response.StatusCode, page)
	}
	if service.request.Limit != 7 || service.request.ProjectID != "project_1" || service.request.WorkItemID != "work_1" || service.request.RunID != "run_1" || service.request.Cursor != "resume" || len(service.request.Kinds) != 2 {
		t.Fatalf("request = %#v", service.request)
	}
}

func TestAttentionCheckpointAPIRejectsUnknownFiltersAndKinds(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingAttentionService{}
	if err := server.SetAttention(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	for _, query := range []string{"?status=pending", "?kind=future", "?limit=201"} {
		request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL()+"/api/v1/attention"+query, nil)
		request.Header.Set("Authorization", endpoint.AuthorizationHeader())
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		assertAPIError(t, response, http.StatusBadRequest, "VALIDATION_FAILED")
		_ = response.Body.Close()
	}
}

func TestAttentionServiceDoesNotChangeLegacyCheckpointCollection(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	attentionService := &recordingAttentionService{}
	approvalService := &recordingApprovalService{}
	if err := server.SetAttention(attentionService); err != nil {
		t.Fatal(err)
	}
	if err := server.SetApprovals(approvalService); err != nil {
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
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || approvalService.listRequest.Status != "pending" {
		t.Fatalf("status=%d legacy request=%#v", response.StatusCode, approvalService.listRequest)
	}
	if len(attentionService.request.Kinds) != 0 || attentionService.request.RunID != "" || attentionService.request.Limit != 0 {
		t.Fatalf("legacy collection called attention service: %#v", attentionService.request)
	}
}

type recordingAttentionService struct {
	page    attention.Page
	request attention.ListRequest
}

func (service *recordingAttentionService) List(_ context.Context, request attention.ListRequest) (attention.Page, error) {
	service.request = request
	if request.Limit > 200 || (len(request.Kinds) == 1 && request.Kinds[0] == "future") {
		return attention.Page{}, attention.ErrInvalidRequest
	}
	return service.page, nil
}
