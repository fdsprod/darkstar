package ports

import "fmt"

// FailureCode is a stable, machine-readable classification at a port boundary.
// Callers must branch on Code rather than adapter error text.
type FailureCode string

const (
	FailureUnavailable       FailureCode = "unavailable"
	FailureUnauthenticated   FailureCode = "unauthenticated"
	FailureInvalidRequest    FailureCode = "invalid_request"
	FailurePermissionDenied  FailureCode = "permission_denied"
	FailureConflict          FailureCode = "conflict"
	FailureNotFound          FailureCode = "not_found"
	FailureUnsupported       FailureCode = "unsupported"
	FailureResourceExhausted FailureCode = "resource_exhausted"
	FailureTimeout           FailureCode = "timeout"
	FailureInterrupted       FailureCode = "interrupted"
	FailureCancelled         FailureCode = "cancelled"
	FailureProtocolDrift     FailureCode = "protocol_drift"
	FailureUncertain         FailureCode = "uncertain"
	FailureInternal          FailureCode = "internal"
)

// Failure is the safe error form returned across a port boundary. Details must
// not contain credentials, raw prompts, artifact content, or provider payloads.
type Failure struct {
	Code      FailureCode
	Message   string
	Retryable bool
	Details   map[string]string
}

func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	if f.Message == "" {
		return string(f.Code)
	}
	return fmt.Sprintf("%s: %s", f.Code, f.Message)
}
