package capabilityregistry

import (
	"context"
	"time"
)

// ObservationScope is the policy boundary that exposed a provider capability.
type ObservationScope string

const (
	ObservationCodex   ObservationScope = "codex"
	ObservationProject ObservationScope = "project"
	ObservationUser    ObservationScope = "user"
)

// Observation is one capability reported by an admitted provider host. Name is
// the provider-native identifier below Scope; observation alone grants no use.
type Observation struct {
	Name            string
	Kind            Kind
	Scope           ObservationScope
	DeclaredVersion string
	Fingerprint     string
	Source          Source
	Interfaces      Interfaces
	Dependencies    []string
	Risk            Risk
	Availability    Availability
	ObservedAt      time.Time
}

// ObservationSnapshot binds discovery results to the exact provider host and
// workspace fingerprint that produced them.
type ObservationSnapshot struct {
	Provider        string
	HostFingerprint string
	Capabilities    []Observation
}

// Observer reports normalized provider capabilities without granting them.
type Observer interface {
	ObserveCapabilities(context.Context) (ObservationSnapshot, error)
}
