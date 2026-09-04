package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

func TestInputRequestAPIExposesGlobalQueueAndServerOwnedRetry(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingInputService{view: inputViewFixture()}
	if err := server.SetInputRequests(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL()+"/api/v1/input-requests", nil)
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-key") || strings.Contains(string(body), "actionKey") {
		t.Fatalf("response leaked provider key: %s", body)
	}
	var list runexecution.InputRequestList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || service.status != statestore.InputRequestPending || len(list.Items) != 1 || list.Items[0].AllowedActions[0] != runexecution.InputRequestActionRetryDelivery {
		t.Fatalf("queue status=%d requested=%q list=%#v", response.StatusCode, service.status, list)
	}
	retry, _ := http.NewRequest(http.MethodPost, endpoint.BaseURL()+"/api/v1/input-requests/"+service.view.ID+"/delivery-retries", nil)
	retry.Header.Set("Authorization", endpoint.AuthorizationHeader())
	retry.Header.Set("If-Match", `"2"`)
	retryResponse, err := http.DefaultClient.Do(retry)
	if err != nil {
		t.Fatal(err)
	}
	defer retryResponse.Body.Close()
	if retryResponse.StatusCode != http.StatusOK || service.retriedID != service.view.ID || service.expected != 2 {
		t.Fatalf("retry status=%d id=%q expected=%d", retryResponse.StatusCode, service.retriedID, service.expected)
	}
}

type recordingInputService struct {
	view      runexecution.InputRequestView
	status    statestore.InputRequestStatus
	retriedID string
	expected  uint64
}

func (service *recordingInputService) InputRequest(context.Context, string) (runexecution.InputRequestView, error) {
	return service.view, nil
}
func (service *recordingInputService) InputRequests(_ context.Context, status statestore.InputRequestStatus) (runexecution.InputRequestList, error) {
	service.status = status
	return runexecution.InputRequestList{SchemaVersion: 1, Items: []runexecution.InputRequestView{service.view}}, nil
}
func (service *recordingInputService) InputRequestsForRun(context.Context, string) (runexecution.InputRequestList, error) {
	return runexecution.InputRequestList{}, nil
}
func (service *recordingInputService) InputRequestsForAttempt(context.Context, string) (runexecution.InputRequestList, error) {
	return runexecution.InputRequestList{}, nil
}
func (service *recordingInputService) AnswerInput(context.Context, runexecution.AnswerInputRequest) (runexecution.InputRequestView, error) {
	return service.view, nil
}
func (service *recordingInputService) RetryInputDelivery(_ context.Context, id string, expected uint64) (runexecution.InputRequestView, error) {
	service.retriedID, service.expected = id, expected
	return service.view, nil
}

func inputViewFixture() runexecution.InputRequestView {
	return runexecution.InputRequestView{ID: "input_00000000000000000000000000", RunID: "run_00000000000000000000000000",
		AttemptID: "attempt_00000000000000000000000000", NodeID: "design", ProviderThreadID: "thread", ProviderRequestID: "opaque",
		ScopeDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Request: statestore.JSONSnapshot(`{"question":"Proceed?"}`),
		Status: statestore.InputRequestAnswerRecorded, Answer: &statestore.InputAnswerProjection{Answer: statestore.JSONSnapshot(`"yes"`), ActionKey: "secret-key", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, RecordedAt: time.Now().UTC()},
		AllowedActions: []runexecution.InputRequestAction{runexecution.InputRequestActionRetryDelivery}, ResourceVersion: 2, LastGlobalPosition: 2, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}
