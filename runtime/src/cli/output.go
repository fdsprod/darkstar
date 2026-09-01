package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	clientapi "github.com/fdsprod/darkstar/runtime/src/api/client"
)

const machineSchemaVersion = 1

type machineErrorOutput struct {
	SchemaVersion int          `json:"schemaVersion"`
	Error         machineError `json:"error"`
}

type machineError struct {
	Code      string                  `json:"code"`
	Message   string                  `json:"message"`
	RequestID string                  `json:"requestId,omitempty"`
	Retryable bool                    `json:"retryable"`
	Details   []clientapi.ErrorDetail `json:"details,omitempty"`
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeCommandError(stdout, stderr io.Writer, jsonOutput bool, command, code, message string, retryable bool, class ExitClass) int {
	if jsonOutput {
		if err := writeJSON(stdout, machineErrorOutput{
			SchemaVersion: machineSchemaVersion,
			Error:         machineError{Code: code, Message: message, Retryable: retryable},
		}); err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: encode error output: %v\n", command, err)
			return int(ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintf(stderr, "%s: %s\n", command, message)
	}
	return int(class)
}

func writeClientError(stdout, stderr io.Writer, jsonOutput bool, command string, err error) int {
	class := ClassifyError(err)
	code := "INTERNAL_INVARIANT_VIOLATION"
	retryable := false
	message := err.Error()
	var requestID string
	var details []clientapi.ErrorDetail

	var problem *clientapi.APIError
	var failure *clientapi.Failure
	if errors.As(err, &problem) {
		code = problem.Code
		message = problem.Message
		requestID = problem.RequestID
		retryable = problem.Retryable
		details = problem.Details
	} else if errors.As(err, &failure) {
		switch failure.Kind {
		case clientapi.FailureDiscovery, clientapi.FailureUnavailable:
			code = "DAEMON_UNAVAILABLE"
			retryable = true
		case clientapi.FailureIncompatible:
			code = "API_VERSION_UNSUPPORTED"
		case clientapi.FailureProtocol:
			code = "API_PROTOCOL_INVALID"
		}
	}

	if jsonOutput {
		if encodeErr := writeJSON(stdout, machineErrorOutput{
			SchemaVersion: machineSchemaVersion,
			Error: machineError{
				Code:      code,
				Message:   message,
				RequestID: requestID,
				Retryable: retryable,
				Details:   details,
			},
		}); encodeErr != nil {
			_, _ = fmt.Fprintf(stderr, "%s: encode error output: %v\n", command, encodeErr)
			return int(ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintf(stderr, "%s: %s\n", command, message)
	}
	return int(class)
}
