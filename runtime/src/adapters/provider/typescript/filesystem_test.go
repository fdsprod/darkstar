package typescript

import (
	"context"
	"errors"
	"strings"
	"testing"

	"darkstar/src/ports/provider"
)

func TestScopedReadsRejectedBeforePluginProcessOrToolBinding(t *testing.T) {
	adapter := &Adapter{}
	requirement := &provider.ScopedReadRequirement{}
	_, startErr := adapter.StartAttempt(context.Background(), provider.AttemptRequest{Filesystem: requirement})
	_, resumeErr := adapter.ResumeAttempt(context.Background(), provider.ResumeRequest{Filesystem: requirement})
	for _, err := range []error{startErr, resumeErr} {
		if !errors.Is(err, provider.ErrScopedReadUnsupported) {
			t.Fatalf("scoped request admitted to plugin dispatch: %v", err)
		}
	}
	if adapter.client != nil || len(adapter.bindings) != 0 {
		t.Fatal("unsupported request prepared a process or tool binding")
	}
}

func TestHostCapabilityFingerprintCannotBeBypassedOnStartOrResume(t *testing.T) {
	wire := strings.Repeat("a", 64)
	adapter := &Adapter{wireCapabilityFingerprint: wire}
	for _, stale := range []string{wire, strings.Repeat("b", 64)} {
		_, startErr := adapter.StartAttempt(context.Background(), provider.AttemptRequest{CapabilityFingerprint: stale})
		_, resumeErr := adapter.ResumeAttempt(context.Background(), provider.ResumeRequest{CapabilityFingerprint: stale})
		if startErr == nil || resumeErr == nil {
			t.Fatal("stale or plugin-only fingerprint bypassed the effective host contract")
		}
	}
	translated, err := adapter.pluginCapabilityFingerprint(context.Background(), hostCapabilityFingerprint(wire))
	if err != nil || translated != wire {
		t.Fatalf("effective fingerprint did not translate to exact plugin contract: %q %v", translated, err)
	}
	if hostCapabilityFingerprint(wire) == hostCapabilityFingerprint(strings.Repeat("b", 64)) {
		t.Fatal("plugin capability change did not change effective fingerprint")
	}
	if adapter.client != nil || len(adapter.bindings) != 0 {
		t.Fatal("fingerprint failure dispatched provider or tools")
	}
}
