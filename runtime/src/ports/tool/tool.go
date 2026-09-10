// Package tool defines provider-neutral executable tool registrations.
package tool

import (
	"context"
	"darkstar/src/ports/provider"
	"encoding/json"
)

type Tool struct {
	Definition   provider.ToolDefinition
	ResultSchema json.RawMessage
	Invoke       func(context.Context, string, json.RawMessage) (json.RawMessage, error)
}
