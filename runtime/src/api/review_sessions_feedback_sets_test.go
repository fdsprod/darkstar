package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	checkpoint "darkstar/src/core/artifactcheckpoint"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/artifactregistry"
)

func TestReviewFeedbackSetDraftBindsExactCandidateAndRepresentation(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &feedbackSetApprovalService{recordingApprovalService: &recordingApprovalService{session: feedbackSetSession()}}
	if err := server.SetApprovals(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	body := `{"candidate":{"artifactId":"artifact_candidate","version":3},"candidateDigest":"` + strings.Repeat("a", 64) + `","scopeDigest":"` + strings.Repeat("b", 64) + `","policyDigest":"` + strings.Repeat("c", 64) + `","representation":{"representationId":"representation_safe","digest":"` + strings.Repeat("d", 64) + `","disclosure":"redacted"}}`
	response := reviewFeedbackSetRequest(t, endpoint, "/api/v1/review-sessions/approval_00000000000000000000000000/feedback-sets", body, "draft-key", `"7"`)
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusCreated || !strings.Contains(response.Header.Get("Location"), "/feedback-sets/feedbackset_") {
		t.Fatalf("status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	var draft draftFeedbackSet
	if err := json.NewDecoder(response.Body).Decode(&draft); err != nil {
		t.Fatal(err)
	}
	if draft.State != "draft" || draft.ApprovalID != service.session.ID || draft.Candidate != service.session.Candidate || draft.PolicyDigest != service.session.PolicyDigest || draft.Representation.RepresentationID != "representation_safe" || draft.Annotations == nil {
		t.Fatalf("draft = %#v", draft)
	}
}

func TestReviewFeedbackSetSubmitPassesCompleteSetAndMapsStaleCandidate(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &feedbackSetApprovalService{recordingApprovalService: &recordingApprovalService{session: feedbackSetSession()}}
	if err := server.SetApprovals(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	body := `{"feedbackSetId":"feedbackset_00000000000000000000000000","candidate":{"artifactId":"artifact_candidate","version":3},"candidateDigest":"` + strings.Repeat("a", 64) + `","scopeDigest":"` + strings.Repeat("b", 64) + `","policyDigest":"` + strings.Repeat("c", 64) + `","representation":{"representationId":"representation_safe","digest":"` + strings.Repeat("d", 64) + `","disclosure":"redacted"},"overallInstruction":"Address both comments as one revision.","annotations":[{"id":"annotation_one","anchor":{"startOffset":2,"endOffset":8,"quoteDigest":"` + strings.Repeat("e", 64) + `","quotedText":"review"},"comment":"Clarify this claim."}]}`
	path := "/api/v1/review-sessions/approval_00000000000000000000000000/feedback-sets/feedbackset_00000000000000000000000000/submit"
	response := reviewFeedbackSetRequest(t, endpoint, path, body, "submit-key", `"7"`)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if service.feedback.FeedbackSetID != "feedbackset_00000000000000000000000000" || service.feedback.Candidate != service.session.Candidate || service.feedback.PolicyDigest != service.session.PolicyDigest || service.feedback.OverallInstruction != "Address both comments as one revision." || len(service.feedback.Annotations) != 1 {
		t.Fatalf("feedback = %#v", service.feedback)
	}
	service.err = checkpoint.ErrCandidateConflict
	response = reviewFeedbackSetRequest(t, endpoint, path, body, "stale-key", `"7"`)
	assertAPIError(t, response, http.StatusConflict, "APPROVAL_STALE_CANDIDATE")
	_ = response.Body.Close()
	service.err = checkpoint.ErrCheckpointConflict
	response = reviewFeedbackSetRequest(t, endpoint, path, body, "version-key", `"7"`)
	defer func() {
		_ = response.Body.Close()
	}()
	assertAPIError(t, response, http.StatusConflict, "APPROVAL_VERSION_CONFLICT")
}

func TestReviewFeedbackSetSubmitAcceptsCompletePayloadAboveLegacyBodyLimit(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &feedbackSetApprovalService{recordingApprovalService: &recordingApprovalService{session: feedbackSetSession()}}
	if err := server.SetApprovals(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	annotations := make([]checkpointport.FeedbackAnnotation, 3)
	for index := range annotations {
		annotations[index] = checkpointport.FeedbackAnnotation{ID: "annotation_large", Anchor: checkpointport.TextRangeAnchor{
			StartOffset: uint64(index * 12000), EndOffset: uint64((index + 1) * 12000), QuoteDigest: strings.Repeat("e", 64), QuotedText: strings.Repeat("x", 12000)}, Comment: "Revise this range."}
	}
	body, err := json.Marshal(map[string]any{"feedbackSetId": "feedbackset_00000000000000000000000000", "candidate": service.session.Candidate,
		"candidateDigest": service.session.CandidateDigest, "scopeDigest": service.session.ScopeDigest, "policyDigest": service.session.PolicyDigest,
		"representation":     map[string]any{"representationId": "representation_safe", "digest": strings.Repeat("d", 64), "disclosure": "redacted"},
		"overallInstruction": "Address every annotation.", "annotations": annotations})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 32769 {
		t.Fatalf("test payload = %d bytes", len(body))
	}
	path := "/api/v1/review-sessions/approval_00000000000000000000000000/feedback-sets/feedbackset_00000000000000000000000000/submit"
	response := reviewFeedbackSetRequest(t, endpoint, path, string(body), "large-key", `"7"`)
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK || len(service.feedback.Annotations) != 3 {
		t.Fatalf("status=%d annotations=%d", response.StatusCode, len(service.feedback.Annotations))
	}
}

func feedbackSetSession() checkpointport.ReviewSession {
	return checkpointport.ReviewSession{SchemaVersion: 1, ID: "approval_00000000000000000000000000", Candidate: artifactregistry.VersionRef{ArtifactID: "artifact_candidate", Version: 3}, CandidateDigest: strings.Repeat("a", 64), ScopeDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), ResourceVersion: 7, State: checkpointport.ReviewAwaitingHuman}
}

type feedbackSetApprovalService struct {
	*recordingApprovalService
}

func (*feedbackSetApprovalService) ValidateFeedbackSetDraft(context.Context, checkpoint.FeedbackDraftRequest) error {
	return nil
}

func reviewFeedbackSetRequest(t *testing.T, endpoint Endpoint, path, body, key, match string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL()+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("If-Match", match)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
