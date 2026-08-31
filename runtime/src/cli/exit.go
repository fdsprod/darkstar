package cli

import (
	"errors"
	"net/http"
	"strings"

	clientapi "github.com/fdsprod/darkstar/runtime/src/api/client"
)

// ExitClass is the frozen process-exit contract for CLI automation.
type ExitClass int

const (
	ExitSuccess             ExitClass = 0
	ExitInvalidInput        ExitClass = 2
	ExitNotFound            ExitClass = 3
	ExitConflict            ExitClass = 4
	ExitInputRequired       ExitClass = 5
	ExitProviderUnavailable ExitClass = 6
	ExitValidationFailed    ExitClass = 7
	ExitTransientFailure    ExitClass = 8
	ExitInvariantViolation  ExitClass = 10
)

// ClassifyError maps API and transport failures into the stable CLI exit
// classes. Command-specific validation should return ExitInvalidInput directly.
func ClassifyError(err error) ExitClass {
	if err == nil {
		return ExitSuccess
	}
	var problem *clientapi.APIError
	if errors.As(err, &problem) {
		return classifyAPIError(problem)
	}
	var failure *clientapi.Failure
	if errors.As(err, &failure) {
		switch failure.Kind {
		case clientapi.FailureIncompatible:
			return ExitConflict
		case clientapi.FailureDiscovery, clientapi.FailureUnavailable:
			return ExitTransientFailure
		case clientapi.FailureProtocol:
			return ExitInvariantViolation
		default:
			return ExitInvariantViolation
		}
	}
	return ExitInvariantViolation
}

func classifyAPIError(problem *clientapi.APIError) ExitClass {
	switch problem.Code {
	case "NOT_FOUND":
		return ExitNotFound
	case "CONFLICT", "INVALID_STATE_TRANSITION", "API_VERSION_UNSUPPORTED", "PRECONDITION_FAILED":
		return ExitConflict
	case "CHECKPOINT_REQUIRED", "INPUT_REQUIRED":
		return ExitInputRequired
	case "VALIDATION_FAILED":
		return ExitValidationFailed
	case "INTERNAL_INVARIANT_VIOLATION":
		return ExitInvariantViolation
	}
	if strings.HasPrefix(problem.Code, "PROVIDER_") {
		return ExitProviderUnavailable
	}
	switch problem.HTTPStatus {
	case http.StatusBadRequest:
		return ExitInvalidInput
	case http.StatusNotFound:
		return ExitNotFound
	case http.StatusConflict, http.StatusPreconditionFailed, http.StatusUpgradeRequired:
		return ExitConflict
	case http.StatusPreconditionRequired:
		return ExitInputRequired
	case http.StatusUnprocessableEntity:
		return ExitValidationFailed
	default:
		return ExitTransientFailure
	}
}
