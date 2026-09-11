// Package plugin defines provider-neutral tool contributions and scoped host calls.
package plugin

import (
	"context"
	"darkstar/src/ports/extension"
	"encoding/json"
)

const Protocol = "darkstar.plugin/v1"

type Tool struct {
	ExecutionKind        string          `json:"executionKind,omitempty"`
	ID                   string          `json:"id"`
	Description          string          `json:"description"`
	InputSchema          json.RawMessage `json:"inputSchema"`
	ResultSchema         json.RawMessage `json:"resultSchema"`
	RequiredCapabilities []string        `json:"requiredCapabilities"`
}
type Resource struct {
	Kind             string   `json:"kind"`
	Category         string   `json:"category"`
	Tool             Tool     `json:"tool"`
	CreateOperation  string   `json:"createOperation"`
	UpdateOperations []string `json:"updateOperations"`
}
type Descriptor struct {
	Ref       extension.Ref `json:"ref"`
	Protocol  string        `json:"protocol"`
	Resources []Resource    `json:"resources"`
	Tools     []Tool        `json:"tools,omitempty"`
	// Nodes use the same bounded invocation transport, but are selected by
	// daemon node dispatch and never exposed as agent tools.
	Nodes []Tool `json:"nodes,omitempty"`
}
type Invocation struct {
	Contribution string          `json:"contribution"`
	Arguments    json.RawMessage `json:"arguments"`
}
type HostServices interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
}
type HostServicesFunc func(context.Context, string, json.RawMessage) (json.RawMessage, error)

func (f HostServicesFunc) Call(ctx context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
	return f(ctx, method, args)
}

type Runtime interface {
	Describe(context.Context) (Descriptor, error)
	Invoke(context.Context, Invocation, HostServices) (json.RawMessage, error)
}
