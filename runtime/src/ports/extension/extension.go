// Package extension defines immutable extension identity, never execution authority.
package extension

import (
	"encoding/json"
	"fmt"
	"regexp"
)

const Protocol = "darkstar.extension/v1"

// Ref pins an implementation. There is deliberately no floating/latest form.
type Ref struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9.-]*/[a-z][a-z0-9._-]*$`)
var version = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (r Ref) Validate() error {
	if !identifier.MatchString(r.ID) || !version.MatchString(r.Version) || !digest.MatchString(r.Digest) {
		return fmt.Errorf("EXTENSION_INVALID_REF: expected namespaced ID, exact version, and SHA-256 digest")
	}
	return nil
}

// Descriptor is discovery data. Required capabilities are requests; the host
// still intersects them with policy before invoking a family-specific port.
type Descriptor struct {
	Ref                  Ref             `json:"ref"`
	Protocol             string          `json:"protocol"`
	DisplayName          string          `json:"displayName"`
	ConfigurationSchema  json.RawMessage `json:"configurationSchema"`
	RequiredCapabilities []string        `json:"requiredCapabilities"`
}

func (d Descriptor) Validate() error {
	if err := d.Ref.Validate(); err != nil {
		return err
	}
	if d.Protocol != Protocol {
		return fmt.Errorf("EXTENSION_INCOMPATIBLE: %q", d.Protocol)
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(d.ConfigurationSchema, &schema) != nil || schema == nil {
		return fmt.Errorf("EXTENSION_INVALID_SCHEMA: configuration schema must be an object")
	}
	return nil
}
