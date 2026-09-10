package nodes

import "errors"

// A routing assessment can recommend only authored transitions. The scheduler
// owns validation and activation; this executor is not yet wired.
func routingUnsupported() error {
	return errors.New("workflow node type routing has no production execution handler")
}
