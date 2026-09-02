package cli_test

import (
	"errors"
	"net/http"
	"testing"

	clientapi "darkstar/src/api/client"
	"darkstar/src/cli"
)

func TestClassifyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want cli.ExitClass
	}{
		{"success", nil, cli.ExitSuccess},
		{"not found", &clientapi.APIError{HTTPStatus: http.StatusNotFound, Code: "NOT_FOUND"}, cli.ExitNotFound},
		{"conflict", &clientapi.APIError{HTTPStatus: http.StatusConflict, Code: "CONFLICT"}, cli.ExitConflict},
		{"checkpoint", &clientapi.APIError{HTTPStatus: http.StatusPreconditionRequired, Code: "CHECKPOINT_REQUIRED"}, cli.ExitInputRequired},
		{"provider", &clientapi.APIError{HTTPStatus: http.StatusServiceUnavailable, Code: "PROVIDER_AUTH_REQUIRED"}, cli.ExitProviderUnavailable},
		{"validation", &clientapi.APIError{HTTPStatus: http.StatusUnprocessableEntity, Code: "VALIDATION_FAILED"}, cli.ExitValidationFailed},
		{"transport", &clientapi.Failure{Kind: clientapi.FailureUnavailable, Op: "test", Err: errors.New("offline")}, cli.ExitTransientFailure},
		{"incompatible", &clientapi.Failure{Kind: clientapi.FailureIncompatible, Op: "test", Err: errors.New("version")}, cli.ExitConflict},
		{"protocol", &clientapi.Failure{Kind: clientapi.FailureProtocol, Op: "test", Err: errors.New("invalid")}, cli.ExitInvariantViolation},
		{"unknown", errors.New("unknown"), cli.ExitInvariantViolation},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := cli.ClassifyError(test.err); got != test.want {
				t.Fatalf("ClassifyError() = %d, want %d", got, test.want)
			}
		})
	}
}
