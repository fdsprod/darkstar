package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/attention"
)

var (
	attentionApprovalPattern = regexp.MustCompile(`^approval_[0-9A-HJKMNP-TV-Z]{26}$`)
	attentionDigestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type attentionJSONClient interface {
	DoJSON(context.Context, string, string, any, any, ...clientapi.RequestOption) error
}

type attentionCommand struct {
	kind           attention.Kind
	approvalID     string
	action         attention.DecisionAction
	comment        string
	idempotencyKey string
}

type attentionDecisionBody struct {
	Action       attention.DecisionAction `json:"action"`
	ScopeDigest  string                   `json:"scopeDigest"`
	PolicyDigest string                   `json:"policyDigest"`
	Comment      string                   `json:"comment,omitempty"`
}

func runAttention(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	request, err := parseAttention(args)
	if err != nil {
		return reviewArgumentError(stdout, stderr, jsonOutput, "darkstar attention", err)
	}
	session, code := connectRunSession("darkstar attention decide", jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	return executeAttention(context.Background(), session, request, jsonOutput, stdout, stderr)
}

func parseAttention(args []string) (attentionCommand, error) {
	if len(args) < 4 || args[0] != "decide" {
		return attentionCommand{}, errors.New("expected attention decide <workflow_control|external_delivery> <approval-id> <approve|deny|cancel>")
	}
	result := attentionCommand{kind: attention.Kind(args[1]), approvalID: args[2], action: attention.DecisionAction(args[3])}
	if result.kind != attention.KindWorkflowControl && result.kind != attention.KindExternalDelivery {
		return attentionCommand{}, errors.New("attention kind must be workflow_control or external_delivery")
	}
	if !attentionApprovalPattern.MatchString(result.approvalID) {
		return attentionCommand{}, errors.New("attention decision requires a canonical approval_ ULID")
	}
	if result.action != attention.DecisionApprove && result.action != attention.DecisionDeny && result.action != attention.DecisionCancel {
		return attentionCommand{}, errors.New("attention action must be approve, deny, or cancel")
	}
	flags, err := parseReviewFilters(args[4:], map[string]string{"--comment": "comment", "--idempotency-key": "key"})
	if err != nil {
		return attentionCommand{}, err
	}
	result.comment = flags.Get("comment")
	if strings.TrimSpace(result.comment) != result.comment || len(result.comment) > 4096 {
		return attentionCommand{}, errors.New("--comment must not have surrounding whitespace and may contain at most 4096 bytes")
	}
	result.idempotencyKey = flags.Get("key")
	if result.idempotencyKey == "" {
		result.idempotencyKey = newIdempotencyKey()
	} else if strings.TrimSpace(result.idempotencyKey) != result.idempotencyKey || len(result.idempotencyKey) < 8 || len(result.idempotencyKey) > 128 {
		return attentionCommand{}, errors.New("--idempotency-key must be between 8 and 128 bytes without surrounding whitespace")
	}
	return result, nil
}

func executeAttention(ctx context.Context, client attentionJSONClient, command attentionCommand, jsonOutput bool, stdout, stderr io.Writer) int {
	commandName := "darkstar attention decide"
	query := url.Values{"itemId": {command.approvalID}, "kind": {string(command.kind)}, "limit": {"1"}}
	var page attention.Page
	if err := client.DoJSON(ctx, http.MethodGet, "attention?"+query.Encode(), nil, &page); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, commandName, err)
	}
	if len(page.Items) != 1 || page.Items[0].Common().ID != command.approvalID || page.Items[0].Common().Kind != command.kind {
		return writeCommandError(stdout, stderr, jsonOutput, commandName, "ATTENTION_STALE", "the exact unresolved attention item is unavailable", false, ExitConflict)
	}
	scopeDigest, policyDigest, allowed := attentionDecisionBinding(page.Items[0], command.action)
	if !allowed || page.Items[0].Common().ResourceVersion == 0 || !attentionDigestPattern.MatchString(scopeDigest) || !attentionDigestPattern.MatchString(policyDigest) {
		return writeCommandError(stdout, stderr, jsonOutput, commandName, "ATTENTION_STALE", "the requested action is not currently allowed", false, ExitConflict)
	}
	body := attentionDecisionBody{Action: command.action, ScopeDigest: scopeDigest, PolicyDigest: policyDigest, Comment: command.comment}
	var result attention.Resolution
	resource := "attention/" + url.PathEscape(string(command.kind)) + "/" + url.PathEscape(command.approvalID) + "/decisions"
	if err := client.DoJSON(ctx, http.MethodPost, resource, body, &result,
		clientapi.WithHeader("Idempotency-Key", command.idempotencyKey),
		clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, page.Items[0].Common().ResourceVersion))); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, commandName, err)
	}
	return writeReviewResult(result, fmt.Sprintf("Recorded %s for %s.", result.Action, result.ID), jsonOutput, stdout, stderr, commandName)
}

func attentionDecisionBinding(item attention.Checkpoint, action attention.DecisionAction) (string, string, bool) {
	switch current := item.(type) {
	case *attention.WorkflowControl:
		for _, allowed := range current.AllowedActions {
			if string(allowed) == string(action) {
				return current.Subject.ScopeDigest, current.Subject.PolicyDigest, true
			}
		}
	case *attention.ExternalDelivery:
		for _, allowed := range current.AllowedActions {
			if string(allowed) == string(action) {
				return current.Subject.ScopeDigest, current.Subject.PolicyDigest, true
			}
		}
	}
	return "", "", false
}
