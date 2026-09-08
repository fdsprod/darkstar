package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/attention"
)

func TestParseAttentionDecision(t *testing.T) {
	request, err := parseAttention([]string{"decide", "workflow_control", "approval_00000000000000000000000000", "deny", "--comment", "Outside scope.", "--idempotency-key", "stable-key"})
	if err != nil {
		t.Fatal(err)
	}
	if request.kind != attention.KindWorkflowControl || request.action != attention.DecisionDeny || request.comment != "Outside scope." || request.idempotencyKey != "stable-key" {
		t.Fatalf("request = %#v", request)
	}
}

func TestParseAttentionDecisionRejectsUnsupportedClassAndAction(t *testing.T) {
	for _, args := range [][]string{
		{"decide", "provider_permission", "approval_00000000000000000000000000", "deny"},
		{"decide", "external_delivery", "approval_00000000000000000000000000", "request_changes"},
		{"decide", "external_delivery", "approval_short", "approve"},
	} {
		if _, err := parseAttention(args); err == nil {
			t.Fatalf("parseAttention(%q) succeeded", args)
		}
	}
}

func TestExecuteAttentionUsesExactBindingAndConcurrencyHeaders(t *testing.T) {
	item := &attention.ExternalDelivery{
		Envelope:       attention.Envelope{Kind: attention.KindExternalDelivery, ID: "approval_00000000000000000000000000", ResourceVersion: 7},
		Subject:        attention.ExternalDeliverySubject{ScopeDigest: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64)},
		AllowedActions: []attention.ExternalDeliveryAction{attention.ExternalDeliveryApprove},
	}
	client := &recordingAttentionClient{page: attention.Page{SchemaVersion: 2, Items: attention.Checkpoints{item}, TotalCount: 1}}
	var stdout, stderr bytes.Buffer
	code := executeAttention(context.Background(), client, attentionCommand{
		kind: attention.KindExternalDelivery, approvalID: item.ID, action: attention.DecisionApprove,
		comment: "Ship it.", idempotencyKey: "stable-key",
	}, false, &stdout, &stderr)
	if code != int(ExitSuccess) || stderr.Len() != 0 || len(client.calls) != 2 {
		t.Fatalf("code=%d calls=%#v stdout=%q stderr=%q", code, client.calls, stdout.String(), stderr.String())
	}
	if client.calls[0].method != http.MethodGet || client.calls[0].resource != "attention?itemId=approval_00000000000000000000000000&kind=external_delivery&limit=1" {
		t.Fatalf("lookup = %#v", client.calls[0])
	}
	decision := client.calls[1]
	body, ok := decision.body.(attentionDecisionBody)
	if !ok || decision.method != http.MethodPost || decision.resource != "attention/external_delivery/approval_00000000000000000000000000/decisions" || body.ScopeDigest != item.Subject.ScopeDigest || body.PolicyDigest != item.Subject.PolicyDigest || body.Action != attention.DecisionApprove {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.headers.Get("If-Match") != `"7"` || decision.headers.Get("Idempotency-Key") != "stable-key" {
		t.Fatalf("headers = %#v", decision.headers)
	}
}

type attentionClientCall struct {
	method, resource string
	body             any
	headers          http.Header
}

type recordingAttentionClient struct {
	page  attention.Page
	calls []attentionClientCall
}

func (client *recordingAttentionClient) DoJSON(_ context.Context, method, resource string, body, response any, options ...clientapi.RequestOption) error {
	request, _ := http.NewRequest(method, "http://localhost/", nil)
	for _, option := range options {
		option(request)
	}
	client.calls = append(client.calls, attentionClientCall{method: method, resource: resource, body: body, headers: request.Header.Clone()})
	if method == http.MethodGet {
		*response.(*attention.Page) = client.page
		return nil
	}
	*response.(*attention.Resolution) = attention.Resolution{Kind: attention.KindExternalDelivery, ID: "approval_00000000000000000000000000", Action: attention.DecisionApprove, ResourceVersion: 8, DecidedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	return nil
}
