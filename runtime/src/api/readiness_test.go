package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/readinesscontrol"
	"darkstar/src/core/routeassessment"
	"darkstar/src/ports/statestore"
)

func TestReadinessAPIQueriesLatestAndRecordsExactDecision(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runs := &recordingRunService{run: apiTestRun()}
	readiness := &recordingReadinessService{view: apiTestReadinessView()}
	if err := server.SetRuns(runs); err != nil {
		t.Fatal(err)
	}
	if err := server.SetReadiness(readiness); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	runID := runs.run.RunID

	shown := readinessRequest(t, endpoint, http.MethodGet, "/api/v1/runs/"+runID+"/readiness", "", "", "")
	if shown.StatusCode != http.StatusOK || shown.Header.Get("ETag") != `"4"` || readiness.latestRunID != runID {
		t.Fatalf("show status=%d etag=%q run=%q", shown.StatusCode, shown.Header.Get("ETag"), readiness.latestRunID)
	}
	_ = shown.Body.Close()

	body := `{"action":"supply_input","assessmentDigest":"` + readiness.view.Assessment.Digest + `","reason":"Provide the declared source.","remedyCode":"missing_source"}`
	decided := readinessRequest(t, endpoint, http.MethodPost, "/api/v1/runs/"+runID+"/readiness/decisions", body, "readiness-decision", `"4"`)
	if decided.StatusCode != http.StatusOK || decided.Header.Get("ETag") != `"5"` {
		t.Fatalf("decide status=%d etag=%q", decided.StatusCode, decided.Header.Get("ETag"))
	}
	_ = decided.Body.Close()
	request := readiness.request
	if request.AssessmentID != readiness.view.Assessment.AssessmentID || request.ExpectedResourceVersion != 4 ||
		request.ExpectedDigest != readiness.view.Assessment.Digest || request.Choice != routeassessment.ChoiceSupplyInput ||
		request.RemedyCode != "missing_source" || request.Reason != "Provide the declared source." ||
		request.IdempotencyKey != "readiness-decision" || request.DecisionID == "" {
		t.Fatalf("decision request = %#v", request)
	}
	replay := readinessRequest(t, endpoint, http.MethodPost, "/api/v1/runs/"+runID+"/readiness/decisions", body, "readiness-decision", `"4"`)
	if replay.StatusCode != http.StatusOK || readiness.request.DecisionID != request.DecisionID {
		t.Fatalf("replay status=%d decisionId=%q, want %q", replay.StatusCode, readiness.request.DecisionID, request.DecisionID)
	}
	_ = replay.Body.Close()
}

func TestReadinessAPIRejectsStaleOrClientAuthoredDecisionState(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runs := &recordingRunService{run: apiTestRun()}
	readiness := &recordingReadinessService{view: apiTestReadinessView()}
	if err := server.SetRuns(runs); err != nil {
		t.Fatal(err)
	}
	if err := server.SetReadiness(readiness); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	base := "/api/v1/runs/" + runs.run.RunID + "/readiness/decisions"
	digest := readiness.view.Assessment.Digest

	stale := readinessRequest(t, endpoint, http.MethodPost, base, `{"action":"continue","assessmentDigest":"`+digest+`","reason":"Proceed."}`, "stale-decision", `"3"`)
	assertAPIError(t, stale, http.StatusPreconditionFailed, "READINESS_VERSION_CONFLICT")
	_ = stale.Body.Close()

	changed := readinessRequest(t, endpoint, http.MethodPost, base, `{"action":"continue","assessmentDigest":"`+strings.Repeat("b", 64)+`","reason":"Proceed."}`, "changed-decision", `"4"`)
	assertAPIError(t, changed, http.StatusPreconditionFailed, "READINESS_ASSESSMENT_CHANGED")
	_ = changed.Body.Close()

	for name, body := range map[string]string{
		"unknown action":     `{"action":"reroute","assessmentDigest":"` + digest + `","reason":"Proceed."}`,
		"patch operations":   `{"action":"accept_route_change","assessmentDigest":"` + digest + `","reason":"Proceed.","operations":[]}`,
		"missing remedy":     `{"action":"supply_input","assessmentDigest":"` + digest + `","reason":"Proceed."}`,
		"remedy on continue": `{"action":"continue","assessmentDigest":"` + digest + `","reason":"Proceed.","remedyCode":"missing_source"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := readinessRequest(t, endpoint, http.MethodPost, base, body, "invalid-decision", `"4"`)
			assertAPIError(t, response, http.StatusUnprocessableEntity, "READINESS_DECISION_INVALID")
			_ = response.Body.Close()
		})
	}
}

func TestReadinessAPIMapsMissingAndDecisionConflicts(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runs := &recordingRunService{run: apiTestRun()}
	readiness := &recordingReadinessService{view: apiTestReadinessView(), err: statestore.ErrNotFound}
	if err := server.SetRuns(runs); err != nil {
		t.Fatal(err)
	}
	if err := server.SetReadiness(readiness); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	resource := "/api/v1/runs/" + runs.run.RunID + "/readiness"

	missing := readinessRequest(t, endpoint, http.MethodGet, resource, "", "", "")
	assertAPIError(t, missing, http.StatusNotFound, "READINESS_NOT_FOUND")
	_ = missing.Body.Close()

	readiness.err = nil
	readiness.decideErr = readinesscontrol.ErrAlreadyDecided
	body := `{"action":"continue","assessmentDigest":"` + readiness.view.Assessment.Digest + `","reason":"Proceed."}`
	conflict := readinessRequest(t, endpoint, http.MethodPost, resource+"/decisions", body, "already-decided", `"4"`)
	assertAPIError(t, conflict, http.StatusConflict, "READINESS_ALREADY_DECIDED")
	_ = conflict.Body.Close()
}

type recordingReadinessService struct {
	view        readinesscontrol.View
	err         error
	decideErr   error
	latestRunID string
	request     readinesscontrol.DecisionRequest
}

func (service *recordingReadinessService) LatestForRun(_ context.Context, runID string) (readinesscontrol.View, error) {
	service.latestRunID = runID
	if service.err != nil {
		return readinesscontrol.View{}, service.err
	}
	return service.view, nil
}

func (service *recordingReadinessService) Decide(_ context.Context, request readinesscontrol.DecisionRequest) (readinesscontrol.View, error) {
	service.request = request
	if service.decideErr != nil {
		return readinesscontrol.View{}, service.decideErr
	}
	result := service.view
	result.Status = statestore.ReadinessAssessmentDecided
	result.ResourceVersion++
	result.AllowedActions = []readinesscontrol.AllowedAction{}
	return result, nil
}

func apiTestReadinessView() readinesscontrol.View {
	now := time.Now().UTC()
	return readinesscontrol.View{
		Assessment: routeassessment.View{
			AssessmentID: "assessment_00000000000000000000000000",
			RunID:        "run_00000000000000000000000000",
			NodeID:       "review",
			Disposition:  routeassessment.DispositionChoiceRequired,
			Digest:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Status:          statestore.ReadinessAssessmentPending,
		AllowedActions:  []readinesscontrol.AllowedAction{{Choice: routeassessment.ChoiceContinue}},
		ResourceVersion: 4,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func readinessRequest(t *testing.T, endpoint Endpoint, method, resource, body, key, ifMatch string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, endpoint.BaseURL()+resource, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Close = true
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestReadinessDecisionBodyIsClosed(t *testing.T) {
	var body readinessDecisionBody
	err := decodeReadinessDecision([]byte(`{"action":"continue","assessmentDigest":"a","reason":"Proceed.","routePatch":{}}`), &body)
	if err == nil {
		t.Fatal("client-authored route patch was accepted")
	}
	encoded, err := json.Marshal(readinessDecisionBody{Action: routeassessment.ChoiceCancel, AssessmentDigest: "a", Reason: "Stop."})
	if err != nil || bytes.Contains(encoded, []byte("decisionId")) {
		t.Fatalf("wire body = %s, err=%v", encoded, err)
	}
}
