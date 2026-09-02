package codex

import (
	"testing"
	"time"

	providerport "darkstar/src/ports/provider"
	"darkstar/tests/providerconformance"
)

func TestCompletedAdaptersConformance(t *testing.T) {
	t.Run("app_server", func(t *testing.T) {
		providerconformance.Run(t, providerconformance.Suite{
			NewProvider: func(t *testing.T) providerport.Provider {
				t.Helper()
				return newTestAdapter(t, newAppServerScript(`{"answer":"conformant"}`, false))
			},
			NewHealthProvider: func(t *testing.T) providerport.Provider {
				t.Helper()
				return newHealthTestAdapter(t, newHealthProbeScript(true, false))
			},
			Request: func(t *testing.T) providerport.AttemptRequest {
				t.Helper()
				return testAttemptRequest(t.TempDir())
			},
		})
	})

	t.Run("exec", func(t *testing.T) {
		providerconformance.Run(t, providerconformance.Suite{
			NewProvider: func(t *testing.T) providerport.Provider {
				t.Helper()
				return newExecTestAdapter(t, NewMemoryExecRecoveryStore(), func(ExecCommand) (ExecProcess, error) {
					return execFixtureProcess(execSuccessFixture("session-conformance", `{"answer":"conformant"}`)), nil
				})
			},
			Request: func(t *testing.T) providerport.AttemptRequest {
				t.Helper()
				request := testAttemptRequest(t.TempDir())
				request.Timeout = time.Second
				return request
			},
		})
	})
}
