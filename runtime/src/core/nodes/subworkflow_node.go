package nodes

import "errors"

// Child frame activation and recovery require daemon lifecycle integration.
// Do not send a workflow graph to an execution agent as a substitute.
func subworkflowUnsupported() error {
	return errors.New("workflow node type subworkflow has no production execution handler")
}
