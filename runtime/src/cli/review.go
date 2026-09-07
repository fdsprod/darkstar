package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/attention"
	"darkstar/src/core/runexecution"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/representationregistry"
)

var feedbackSetIdentityPattern = regexp.MustCompile(`^feedbackset_[0-9A-HJKMNP-TV-Z]{26}$`)

type feedbackSetDraft struct {
	SchemaVersion      int                                      `json:"schemaVersion"`
	State              string                                   `json:"state"`
	ID                 string                                   `json:"id"`
	ApprovalID         string                                   `json:"approvalId"`
	Candidate          artifactregistry.VersionRef              `json:"candidate"`
	CandidateDigest    string                                   `json:"candidateDigest"`
	ScopeDigest        string                                   `json:"scopeDigest"`
	PolicyDigest       string                                   `json:"policyDigest"`
	Representation     checkpointport.TextRepresentationBinding `json:"representation"`
	OverallInstruction string                                   `json:"overallInstruction"`
	Annotations        []checkpointport.FeedbackAnnotation      `json:"annotations"`
}

func runReview(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar review"
	if len(args) > 0 && args[0] == "feedback-set" {
		return runReviewFeedbackSet(args[1:], jsonOutput, stdout, stderr)
	}
	if len(args) < 2 {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected review show|history|feedback-set|resume|respond|approve|reject <id>"))
	}
	session, code := connectRunSession(command+" "+args[0], jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	if args[0] == "history" {
		if len(args) != 2 || !strings.HasPrefix(args[1], "checkpoint_") {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected review history <checkpoint-id>"))
		}
		var history checkpointport.ReviewHistory
		if err := session.DoJSON(context.Background(), http.MethodGet, "review-sessions?checkpointId="+url.QueryEscape(args[1]), nil, &history); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeReviewResult(history, fmt.Sprintf("%s: %d review session(s).", history.CheckpointID, len(history.Sessions)), jsonOutput, stdout, stderr, command)
	}
	if !strings.HasPrefix(args[1], "approval_") {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("review session ID must start with approval_"))
	}
	var current checkpointport.ReviewSession
	if err := session.DoJSON(context.Background(), http.MethodGet, "review-sessions/"+url.PathEscape(args[1]), nil, &current); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	if args[0] == "show" && len(args) == 2 {
		return writeReviewResult(current, fmt.Sprintf("%s is %s with %d turn(s).", current.ID, current.State, len(current.Turns)), jsonOutput, stdout, stderr, command)
	}
	flags, err := parseReviewFilters(args[2:], map[string]string{"--message": "message", "--comment": "comment", "--attempt": "attempt", "--outcome": "outcome", "--artifact": "artifact", "--version": "version", "--next-approval": "next", "--idempotency-key": "key"})
	if err != nil {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, err)
	}
	key := flags.Get("key")
	if key == "" {
		key = newIdempotencyKey()
	}
	body := map[string]any{"candidateDigest": current.CandidateDigest, "scopeDigest": current.ScopeDigest}
	actionPath := ""
	switch args[0] {
	case "feedback":
		if flags.Get("message") == "" {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("review feedback requires --message"))
		}
		actionPath, body["message"] = "feedback", flags.Get("message")
	case "resume":
		if !strings.HasPrefix(flags.Get("attempt"), "attempt_") {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("review resume requires --attempt <attempt-id>"))
		}
		actionPath, body["attemptId"] = "resume", flags.Get("attempt")
	case "respond":
		outcome := checkpointport.AgentOutcome(flags.Get("outcome"))
		if !strings.HasPrefix(flags.Get("attempt"), "attempt_") || (outcome != checkpointport.AgentRevised && outcome != checkpointport.AgentFailed && outcome != checkpointport.AgentCancelled) {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("review respond requires --attempt and --outcome revised|failed|cancelled"))
		}
		actionPath, body["attemptId"], body["outcome"], body["message"] = "agent-responses", flags.Get("attempt"), outcome, flags.Get("message")
		if outcome == checkpointport.AgentRevised {
			version, parseErr := strconv.ParseUint(flags.Get("version"), 10, 64)
			if parseErr != nil || version == 0 || !strings.HasPrefix(flags.Get("artifact"), "artifact_") || !strings.HasPrefix(flags.Get("next"), "approval_") {
				return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("revised response requires --artifact, --version, and --next-approval"))
			}
			body["candidate"] = map[string]any{"artifactId": flags.Get("artifact"), "version": version}
			body["nextApprovalId"] = flags.Get("next")
		}
	case "approve", "reject":
		actionPath, body["action"], body["policyDigest"], body["comment"] = "decisions", args[0], current.PolicyDigest, flags.Get("comment")
		if args[0] == "reject" && strings.TrimSpace(flags.Get("comment")) == "" {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("review reject requires --comment"))
		}
	default:
		return reviewArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("unknown review command %q", args[0]))
	}
	if err := session.DoJSON(context.Background(), http.MethodPost, "review-sessions/"+url.PathEscape(current.ID)+"/"+actionPath, body, &current,
		clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, current.ResourceVersion))); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeReviewResult(current, fmt.Sprintf("Review session %s is %s.", current.ID, current.State), jsonOutput, stdout, stderr, command)
}

func runReviewFeedbackSet(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar review feedback-set"
	if len(args) < 2 || (args[0] != "create" && args[0] != "submit") || !strings.HasPrefix(args[1], "approval_") {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected review feedback-set create|submit <approval-id>"))
	}
	approvalID := args[1]
	allowed := map[string]string{"--idempotency-key": "key"}
	if args[0] == "create" {
		allowed["--representation"] = "representation"
		allowed["--representation-digest"] = "digest"
		allowed["--disclosure"] = "disclosure"
	} else {
		allowed["--file"] = "file"
	}
	flags, err := parseReviewFilters(args[2:], allowed)
	if err != nil {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, err)
	}
	key := flags.Get("key")
	if key == "" {
		key = newIdempotencyKey()
	}
	var submitDraft feedbackSetDraft
	if args[0] == "submit" {
		submitDraft, err = readFeedbackSetDraft(flags.Get("file"), approvalID)
		if err != nil {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, err)
		}
	}
	session, code := connectRunSession(command+" "+args[0], jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var current checkpointport.ReviewSession
	if err := session.DoJSON(context.Background(), http.MethodGet, "review-sessions/"+url.PathEscape(approvalID), nil, &current); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	if args[0] == "create" {
		disclosure := representationregistry.Disclosure(flags.Get("disclosure"))
		binding := checkpointport.TextRepresentationBinding{RepresentationID: flags.Get("representation"), Digest: flags.Get("digest"), Disclosure: disclosure}
		if !validFeedbackDraftBinding(binding) {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("create requires --representation, --representation-digest, and --disclosure raw|redacted"))
		}
		body := map[string]any{"candidate": current.Candidate, "candidateDigest": current.CandidateDigest, "scopeDigest": current.ScopeDigest, "policyDigest": current.PolicyDigest, "representation": binding}
		var draft feedbackSetDraft
		if err := session.DoJSON(context.Background(), http.MethodPost, "review-sessions/"+url.PathEscape(approvalID)+"/feedback-sets", body, &draft,
			clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, current.ResourceVersion))); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeReviewResult(draft, "Created feedback-set draft "+draft.ID+".", jsonOutput, stdout, stderr, command)
	}
	draft := submitDraft
	body := map[string]any{"feedbackSetId": draft.ID, "candidate": draft.Candidate, "candidateDigest": draft.CandidateDigest,
		"scopeDigest": draft.ScopeDigest, "policyDigest": draft.PolicyDigest, "representation": draft.Representation, "overallInstruction": draft.OverallInstruction, "annotations": draft.Annotations}
	if err := session.DoJSON(context.Background(), http.MethodPost, "review-sessions/"+url.PathEscape(approvalID)+"/feedback-sets/"+url.PathEscape(draft.ID)+"/submit", body, &current,
		clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, current.ResourceVersion))); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeReviewResult(current, "Submitted feedback set "+draft.ID+".", jsonOutput, stdout, stderr, command)
}

func validFeedbackDraftBinding(value checkpointport.TextRepresentationBinding) bool {
	return strings.HasPrefix(value.RepresentationID, "representation_") && validFeedbackSetDigest(value.Digest) &&
		(value.Disclosure == representationregistry.DisclosureRaw || value.Disclosure == representationregistry.DisclosureRedacted)
}

func validFeedbackSetDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func readFeedbackSetDraft(name, approvalID string) (feedbackSetDraft, error) {
	var draft feedbackSetDraft
	if strings.TrimSpace(name) == "" {
		return draft, errors.New("submit requires --file <feedback-set.json>")
	}
	content, err := os.ReadFile(name)
	if err != nil {
		return draft, fmt.Errorf("read feedback set: %w", err)
	}
	if len(content) > 8<<20 {
		return draft, errors.New("feedback-set file exceeds 8 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil || decoder.Decode(new(any)) != io.EOF {
		return draft, errors.New("feedback-set file must contain one valid draft JSON object")
	}
	if draft.SchemaVersion != 1 || draft.State != "draft" || !feedbackSetIdentityPattern.MatchString(draft.ID) || draft.ApprovalID != approvalID ||
		!strings.HasPrefix(draft.Candidate.ArtifactID, "artifact_") || draft.Candidate.Version == 0 || !validFeedbackSetDigest(draft.CandidateDigest) || !validFeedbackSetDigest(draft.ScopeDigest) || !validFeedbackSetDigest(draft.PolicyDigest) ||
		!validFeedbackDraftBinding(draft.Representation) || strings.TrimSpace(draft.OverallInstruction) == "" {
		return draft, errors.New("feedback-set draft must preserve its exact approval, candidate, scope, and representation binding and include an overall instruction")
	}
	return draft, nil
}

func runCheckpoint(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar checkpoint"
	if len(args) == 0 {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("a checkpoint command is required"))
	}
	if args[0] == "approve" || args[0] == "reject" || args[0] == "request-changes" {
		action := args[0]
		if action == "request-changes" {
			action = "request_changes"
		}
		forward := []string{"decide"}
		if len(args) > 1 {
			forward = append(forward, args[1], action)
		}
		for index := 2; index < len(args); index++ {
			if args[index] == "--message" {
				forward = append(forward, "--comment")
			} else {
				forward = append(forward, args[index])
			}
		}
		return runApproval(forward, jsonOutput, stdout, stderr)
	}
	if args[0] == "answer" {
		if len(args) != 4 || args[2] != "--file" {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected checkpoint answer <input-id> --file <answers.json>"))
		}
		content, err := os.ReadFile(args[3])
		if err != nil {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("read answer file: %w", err))
		}
		if len(content) > 1<<20 {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("answer file exceeds 1 MiB"))
		}
		return runInput([]string{"answer", args[1], "--answer", string(content)}, jsonOutput, stdout, stderr)
	}
	session, code := connectRunSession(command+" "+args[0], jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	switch args[0] {
	case "show":
		if len(args) != 2 || !strings.HasPrefix(args[1], "checkpoint_") {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected checkpoint show <checkpoint-id>"))
		}
		var history checkpointport.History
		if err := session.DoJSON(context.Background(), http.MethodGet, "checkpoints/"+url.PathEscape(args[1]), nil, &history); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeReviewResult(history, fmt.Sprintf("%s: %d review round(s).", history.CheckpointID, len(history.Rounds)), jsonOutput, stdout, stderr, command)
	case "list":
		query, err := parseReviewFilters(args[1:], map[string]string{"--run": "runId", "--project": "projectId", "--work": "workItemId", "--kind": "kind", "--limit": "limit", "--cursor": "cursor"})
		if err != nil {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var queue attention.Page
		if err := session.DoJSON(context.Background(), http.MethodGet, "attention?"+query.Encode(), nil, &queue); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeReviewResult(queue, fmt.Sprintf("%d item(s) require attention.", len(queue.Items)), jsonOutput, stdout, stderr, command)
	default:
		return reviewArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("unknown checkpoint command %q", args[0]))
	}
}

func runApproval(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar approval"
	if len(args) < 2 || !strings.HasPrefix(args[1], "approval_") {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected approval show|decide <approval-id>"))
	}
	session, code := connectRunSession(command+" "+args[0], jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var round checkpointport.Round
	if err := session.DoJSON(context.Background(), http.MethodGet, "approvals/"+url.PathEscape(args[1]), nil, &round); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	if args[0] == "show" && len(args) == 2 {
		return writeReviewResult(round, fmt.Sprintf("%s is %s (round %d).", round.ApprovalID, round.State, round.Revision), jsonOutput, stdout, stderr, command)
	}
	if args[0] != "decide" || len(args) < 3 {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected approval decide <approval-id> <approve|request_changes|reject>"))
	}
	action := checkpointport.Action(args[2])
	if action != checkpointport.ActionApprove && action != checkpointport.ActionRequestChanges && action != checkpointport.ActionReject {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("approval action must be approve, request_changes, or reject"))
	}
	options, err := parseReviewFilters(args[3:], map[string]string{"--comment": "comment", "--idempotency-key": "key"})
	if err != nil {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, err)
	}
	comment := options.Get("comment")
	if (action == checkpointport.ActionRequestChanges || action == checkpointport.ActionReject) && strings.TrimSpace(comment) == "" {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("request_changes and reject require --comment"))
	}
	key := options.Get("key")
	if key == "" {
		key = newIdempotencyKey()
	}
	body := map[string]any{"action": action, "scopeDigest": round.ScopeDigest, "policyDigest": round.PolicyDigest, "comment": comment}
	if err := session.DoJSON(context.Background(), http.MethodPost, "approvals/"+url.PathEscape(round.ApprovalID)+"/decisions", body, &round,
		clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, round.ResourceVersion))); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeReviewResult(round, fmt.Sprintf("Recorded %s for %s.", action, round.ApprovalID), jsonOutput, stdout, stderr, command)
}

func runInput(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar input"
	if len(args) == 0 {
		return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("an input command is required"))
	}
	session, code := connectRunSession(command+" "+args[0], jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	switch args[0] {
	case "list":
		query, err := parseReviewFilters(args[1:], map[string]string{"--run": "runId", "--attempt": "attemptId", "--status": "status"})
		if err != nil || (query.Get("runId") != "" && query.Get("attemptId") != "") {
			if err == nil {
				err = errors.New("--run and --attempt are mutually exclusive")
			}
			return reviewArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var list runexecution.InputRequestList
		resource := "input-requests"
		if encoded := query.Encode(); encoded != "" {
			resource += "?" + encoded
		}
		if err := session.DoJSON(context.Background(), http.MethodGet, resource, nil, &list); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeReviewResult(list, fmt.Sprintf("%d input request(s).", len(list.Items)), jsonOutput, stdout, stderr, command)
	case "show", "answer", "retry":
		if len(args) < 2 || !strings.HasPrefix(args[1], "input_") {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected input show|answer|retry <input-id>"))
		}
		var view runexecution.InputRequestView
		if err := session.DoJSON(context.Background(), http.MethodGet, "input-requests/"+url.PathEscape(args[1]), nil, &view); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		if args[0] == "show" && len(args) == 2 {
			return writeReviewResult(view, fmt.Sprintf("%s is %s.", view.ID, view.Status), jsonOutput, stdout, stderr, command)
		}
		if args[0] == "retry" && len(args) == 2 {
			if err := session.DoJSON(context.Background(), http.MethodPost, "input-requests/"+url.PathEscape(view.ID)+"/delivery-retries", nil, &view,
				clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, view.ResourceVersion))); err != nil {
				return writeClientError(stdout, stderr, jsonOutput, command, err)
			}
			return writeReviewResult(view, "Retried provider delivery for "+view.ID+".", jsonOutput, stdout, stderr, command)
		}
		if args[0] != "answer" {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("unexpected input command arguments"))
		}
		options, err := parseReviewFilters(args[2:], map[string]string{"--answer": "answer", "--idempotency-key": "key"})
		if err != nil || options.Get("answer") == "" {
			if err == nil {
				err = errors.New("input answer requires --answer <json>")
			}
			return reviewArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		answer := json.RawMessage(options.Get("answer"))
		if !json.Valid(answer) {
			return reviewArgumentError(stdout, stderr, jsonOutput, command, errors.New("--answer must be valid JSON"))
		}
		key := options.Get("key")
		if key == "" {
			key = newIdempotencyKey()
		}
		body := struct {
			ScopeDigest string          `json:"scopeDigest"`
			Answer      json.RawMessage `json:"answer"`
		}{view.ScopeDigest, answer}
		if err := session.DoJSON(context.Background(), http.MethodPost, "input-requests/"+url.PathEscape(view.ID)+"/answer", body, &view,
			clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, view.ResourceVersion))); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeReviewResult(view, "Recorded answer for "+view.ID+".", jsonOutput, stdout, stderr, command)
	default:
		return reviewArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("unknown input command %q", args[0]))
	}
}

func parseReviewFilters(args []string, supported map[string]string) (url.Values, error) {
	values := url.Values{}
	for len(args) > 0 {
		field, ok := supported[args[0]]
		if !ok || len(args) < 2 || strings.TrimSpace(args[1]) == "" || values.Has(field) {
			return nil, fmt.Errorf("invalid or repeated option %q", args[0])
		}
		values.Set(field, args[1])
		args = args[2:]
	}
	return values, nil
}

func writeReviewResult(value any, human string, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, value); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintln(stdout, human)
	}
	return int(ExitSuccess)
}

func reviewArgumentError(stdout, stderr io.Writer, jsonOutput bool, command string, err error) int {
	return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
}
