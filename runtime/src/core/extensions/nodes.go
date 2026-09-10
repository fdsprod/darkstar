package extensions

import (
	"encoding/json"
	"fmt"

	"darkstar/src/ports/extension"
	"darkstar/src/ports/nodeextension"
	"darkstar/src/ports/valueschema"
)

// NodeCatalog is assembled with host-granted capabilities. Registration cannot
// grant a capability, and configuration is checked before implementation code.
type NodeCatalog struct {
	catalog   *Catalog[nodeextension.Executor]
	validator valueschema.Validator
}

func NewNodeCatalog(validator valueschema.Validator, granted []string, entries ...Registration[nodeextension.Executor]) (*NodeCatalog, error) {
	if validator == nil {
		return nil, fmt.Errorf("node extension schema validator is required")
	}
	catalog, err := NewWithCapabilities(granted, entries...)
	if err != nil {
		return nil, err
	}
	return &NodeCatalog{catalog, validator}, nil
}

func (c *NodeCatalog) Configure(ref extension.Ref, config json.RawMessage) (nodeextension.Executor, error) {
	return c.catalog.Configure(ref, config, c.validator)
}

func (c *NodeCatalog) Descriptors() []extension.Descriptor { return c.catalog.Descriptors() }
