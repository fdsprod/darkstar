// Package capabilityregistry defines immutable capability records and storage.
package capabilityregistry

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("capability record not found")
	ErrConflict = errors.New("capability record conflict")
)

type Kind string

const (
	KindSkill Kind = "skill"
	KindTool  Kind = "tool"
)

type Class string

const (
	ClassGuaranteed           Class = "guaranteed"
	ClassRegistered           Class = "registered"
	ClassInherited            Class = "inherited"
	ClassUnsupportedDiscovery Class = "unsupported_discovery"
)

type Availability string

const (
	AvailabilityAvailable   Availability = "available"
	AvailabilityUnavailable Availability = "unavailable"
	AvailabilityUnhealthy   Availability = "unhealthy"
)

type Source struct {
	Type    string `json:"type"`
	Locator string `json:"locator"`
}

type Interfaces struct {
	Inputs  string `json:"inputs,omitempty"`
	Outputs string `json:"outputs,omitempty"`
}

type Risk struct {
	Reads              bool `json:"reads"`
	Writes             bool `json:"writes"`
	Network            bool `json:"network"`
	ExternalSideEffect bool `json:"externalSideEffect"`
}

// Record is one immutable registry observation. Policy is intentionally absent:
// the same record may be allowed with different permission ceilings by attempts.
type Record struct {
	SchemaVersion   int          `json:"schemaVersion"`
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Kind            Kind         `json:"kind"`
	Class           Class        `json:"class"`
	DeclaredVersion string       `json:"declaredVersion,omitempty"`
	Fingerprint     string       `json:"fingerprint"`
	Source          Source       `json:"source"`
	Interfaces      Interfaces   `json:"interfaces"`
	Dependencies    []string     `json:"dependencies"`
	Risk            Risk         `json:"risk"`
	Availability    Availability `json:"availability"`
	ObservedAt      time.Time    `json:"observedAt"`
}

// Selection is the exact capability and permission grant frozen for an attempt.
type Selection struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        Kind     `json:"kind"`
	Class       Class    `json:"class"`
	Version     string   `json:"version,omitempty"`
	Fingerprint string   `json:"fingerprint"`
	Source      Source   `json:"source"`
	Permissions []string `json:"permissions"`
}

type Registry interface {
	RegisterCapability(context.Context, Record, string) (Record, bool, error)
	Capability(context.Context, string) (Record, error)
	Snapshot(context.Context) ([]Record, error)
}
