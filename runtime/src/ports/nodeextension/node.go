// Package nodeextension defines a bounded custom operation, not a scheduler.
package nodeextension

import (
	"context"
	"encoding/json"

	"darkstar/src/ports/extension"
)

type Request struct {
	Configuration json.RawMessage            `json:"configuration"`
	Inputs        map[string]json.RawMessage `json:"inputs"`
}

// Executor returns named candidate outputs. The daemon validates the complete
// output envelope and commits it through the ordinary attempt lifecycle.
type Executor interface {
	Execute(context.Context, Request) (map[string]json.RawMessage, error)
}

type Resolver interface {
	Configure(extension.Ref, json.RawMessage) (Executor, error)
}
