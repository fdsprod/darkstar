package statestore

import (
	"encoding/json"
	"errors"
	"fmt"
)

const RunExecutionContextSchemaVersion uint64 = 1

// ErrRunExecutionContextRevisionConflict classifies a failed compare-and-swap
// write. Callers must reload the authoritative snapshot before retrying.
var ErrRunExecutionContextRevisionConflict = errors.New("run execution context revision conflict")

type RunExecutionContextRevisionConflictError struct {
	RunID    string
	Expected uint64
	Actual   uint64
}

func (e *RunExecutionContextRevisionConflictError) Error() string {
	return fmt.Sprintf("%s: run %s expected revision %d, found %d", ErrRunExecutionContextRevisionConflict, e.RunID, e.Expected, e.Actual)
}

func (e *RunExecutionContextRevisionConflictError) Unwrap() error {
	return ErrRunExecutionContextRevisionConflict
}

// RunExecutionContext is the single durable scheduler snapshot for one run.
// RunInputs are immutable after creation. AcceptedOutputs and Frame advance
// together under Revision so output binding and transition tokens cannot drift.
// Digest is an integrity checksum calculated by the store over the versioned
// content; it is verified on every read rather than serving as separate truth.
type RunExecutionContext struct {
	SchemaVersion   uint64                                `json:"schemaVersion"`
	RunID           string                                `json:"runId"`
	RunInputs       map[string]json.RawMessage            `json:"runInputs"`
	AcceptedOutputs map[string]map[string]json.RawMessage `json:"acceptedOutputs"`
	// FrameSnapshot is the closed workflow.FrameSnapshot JSON wire object.
	// The storage port owns durable bytes without reversing the core-to-port
	// dependency; workflow code performs the full typed replay validation.
	FrameSnapshot json.RawMessage `json:"frameSnapshot"`
	Revision      uint64          `json:"revision"`
	Digest        string          `json:"digest"`
}
