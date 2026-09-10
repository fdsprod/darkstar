// Package outputvalidator defines deterministic candidate checks.
package outputvalidator

import (
	"context"
	"encoding/json"
)

type Request struct {
	Configuration json.RawMessage
	Inputs        map[string]json.RawMessage
	Outputs       map[string]json.RawMessage
}

// Validator returns inspectable diagnostics. A non-nil error rejects the
// candidate; only the daemon decides whether all required checks passed.
type Validator interface {
	Validate(context.Context, Request) ([]string, error)
}
