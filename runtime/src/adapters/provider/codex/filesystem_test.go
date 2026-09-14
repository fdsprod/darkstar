package codex

import (
	"context"
	"errors"
	"testing"

	"darkstar/src/ports"
	"darkstar/src/ports/provider"
)

func TestCodexScopedReadsRejectedBeforeAnyProviderDispatch(t *testing.T) {
	// Zero adapters deliberately have no factory, executable, recorder, or
	// process state. Admission must reject before consulting any of them.
	for name, adapter := range map[string]provider.Provider{"app-server": &Adapter{}, "exec": &ExecAdapter{}} {
		t.Run(name, func(t *testing.T) {
			for _, requirement := range []*provider.ScopedReadRequirement{{}, {ReadRoots: []string{"untrusted-live-path"}}} {
				_, startErr := adapter.StartAttempt(context.Background(), provider.AttemptRequest{Filesystem: requirement})
				_, resumeErr := adapter.ResumeAttempt(context.Background(), provider.ResumeRequest{Filesystem: requirement})
				for _, err := range []error{startErr, resumeErr} {
					var failure *ports.Failure
					if !errors.As(err, &failure) || failure.Code != ports.FailureUnsupported {
						t.Fatalf("scoped request was not rejected before dispatch: %v", err)
					}
				}
			}
		})
	}
}
