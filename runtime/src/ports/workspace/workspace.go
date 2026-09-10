// Package workspace defines host-bound work-item storage, separate from Git checkouts.
package workspace

import "context"

// Area separates persistent package state from per-attempt mutable working data.
type Area string

const (
	PluginState Area = "plugin_state"
	Scratch     Area = "scratch"
	Staged      Area = "staged"
)

// Grant is supplied by the daemon after authorizing the work item and invocation.
// It is never populated from plugin call arguments. PluginID is the stable package
// identity; state versions belong in file names, not in a machine-specific path.
type Grant struct {
	WorkItemID string
	PluginID   string
	AttemptID  string
}

type Provisioner interface {
	Ensure(context.Context, string) error
}

type Manager interface {
	Provisioner
	Bind(context.Context, Grant) (Handle, error)
}

// Handle exposes no backing paths. WriteFile creates a file exclusively;
// ReplaceFile uses a content revision for atomic compare-and-swap updates.
// Shared logical resources still use the host's revision-checked resource API.
// ReadFile returns an independent bounded snapshot; publishing that snapshot
// remains a separate daemon operation. Closing a handle does not delete data.
type Handle interface {
	ReadFile(context.Context, Area, string) ([]byte, error)
	WriteFile(context.Context, Area, string, []byte) error
	// ReplaceFile atomically updates an existing file only when its current
	// SHA-256 matches expectedDigest. A stale digest leaves prior bytes intact.
	ReplaceFile(context.Context, Area, string, string, []byte) error
	Close() error
}
