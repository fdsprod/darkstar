package routeadvisor_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/adapters/provider/fake"
	"darkstar/src/adapters/routeadvisor/reasoning"
	"darkstar/src/core/identity"
	"darkstar/src/ports"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/routeadvisor"
)

type capturedProvider struct {
	provider.Provider
	request provider.AttemptRequest
}

func (p *capturedProvider) StartAttempt(ctx context.Context, r provider.AttemptRequest) (provider.AttemptHandle, error) {
	p.request = r
	return p.Provider.StartAttempt(ctx, r)
}

func fakeAdvisor(t *testing.T, output string, steps []fake.Step) (reasoning.Advisor, *fake.Fake, *capturedProvider) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	adapter, err := fake.New(fake.Scenario{Attempts: []fake.AttemptScenario{{AttemptID: identity.Deterministic("attempt_", digest), Steps: steps, Result: provider.SucceededResult{StructuredOutput: json.RawMessage(output)}}}})
	if err != nil {
		t.Fatal(err)
	}
	captured := &capturedProvider{Provider: adapter}
	return reasoning.Advisor{Provider: func() (provider.Provider, error) {
		return captured, nil
	}, Workspace: t.TempDir()}, adapter, captured
}

func TestProviderAdviceUsesReadOnlyBoundedAuthorityAndClosedJSON(t *testing.T) {
	a, _, capture := fakeAdvisor(t, `{"confidence":"low","candidates":[],"evidenceUsed":[]}`, nil)
	advice, err := a.Assess(context.Background(), routeadvisor.Request{Digest: strings.Repeat("a", 64), Outcome: "Review only"})
	if err != nil {
		t.Fatal(err)
	}
	if advice.Confidence != "low" {
		t.Fatal(advice)
	}
	r := capture.request
	if r.Access != provider.AccessReadOnly || r.Network != provider.NetworkDenied || r.CommandPolicy != provider.InteractionDeny || r.FilePolicy != provider.InteractionDeny || r.ToolPolicy != provider.InteractionDeny || r.Timeout == 0 || !json.Valid(r.OutputSchema) {
		t.Fatalf("unsafe provider authority=%#v", r)
	}
	for _, output := range []string{`{"confidence":"high","candidates":[],"evidenceUsed":[],"execute":true}`, `{"confidence":"high"} {}`, `not json`} {
		a, _, _ = fakeAdvisor(t, output, nil)
		if _, err := a.Assess(context.Background(), routeadvisor.Request{Digest: strings.Repeat("a", 64)}); err == nil {
			t.Fatalf("accepted malformed provider result %s", output)
		}
	}
}

func TestProviderStreamFailureCancelsAttempt(t *testing.T) {
	a, adapter, _ := fakeAdvisor(t, `{}`, []fake.Step{fake.Fail(&ports.Failure{Code: ports.FailureInterrupted, Message: "stream interrupted"})})
	if _, err := a.Assess(context.Background(), routeadvisor.Request{Digest: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("expected stream failure")
	}
	cancelled := false
	for _, call := range adapter.Calls() {
		if call.Kind == fake.CallCancel {
			cancelled = true
		}
	}
	if !cancelled {
		t.Fatal("orphaned provider assessment attempt")
	}
}

func TestInvalidInputDigestCannotPanicOrStartProvider(t *testing.T) {
	a, adapter, _ := fakeAdvisor(t, `{}`, nil)
	if _, err := a.Assess(context.Background(), routeadvisor.Request{Digest: "short"}); err == nil {
		t.Fatal("accepted invalid digest")
	}
	if len(adapter.Calls()) != 0 {
		t.Fatal("invalid request invoked provider")
	}
}
