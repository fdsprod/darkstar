package nodes

import "errors"

// Standalone approval execution is not yet wired. Artifact checkpoints remain
// daemon-owned and continue to use the existing durable approval lifecycle.
func approvalUnsupported() error {
	return errors.New("workflow node type approval has no standalone execution handler; artifact checkpoints are supported")
}
