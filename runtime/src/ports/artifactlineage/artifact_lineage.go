// Package artifactlineage defines exact-version dependencies and freshness.
package artifactlineage

import (
	"context"
	"errors"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

var (
	// ErrDependencyConflict means the exact edge already has another impact.
	ErrDependencyConflict = errors.New("artifact dependency conflict")
	// ErrDependencyCycle means an edge would make exact-version lineage cyclic.
	ErrDependencyCycle = errors.New("artifact dependency cycle")
)

// Impact describes what an upstream revision means for a dependent version.
type Impact string

const (
	ImpactPotentiallyStale Impact = "potentially_stale"
	ImpactInvalidated      Impact = "invalidated"
)

// Freshness is derived from durable invalidation observations. Current is
// represented by the absence of an observation, not by a mutable status row.
type Freshness string

const (
	FreshnessCurrent          Freshness = "current"
	FreshnessPotentiallyStale Freshness = "potentially_stale"
	FreshnessInvalidated      Freshness = "invalidated"
)

// Dependency is one immutable directed edge between exact artifact versions.
type Dependency struct {
	Source    artifactregistry.VersionRef `json:"source"`
	Dependent artifactregistry.VersionRef `json:"dependent"`
	Impact    Impact                      `json:"impact"`
	CreatedAt time.Time                   `json:"createdAt"`
}

// AddRequest describes one exact-version dependency.
type AddRequest struct {
	Source    artifactregistry.VersionRef
	Dependent artifactregistry.VersionRef
	Impact    Impact
	CreatedAt time.Time
}

// Invalidation records the effect of one upstream revision on one descendant.
// Trigger names the new upstream version; the superseded version is Trigger-1.
type Invalidation struct {
	Trigger    artifactregistry.VersionRef `json:"trigger"`
	Descendant artifactregistry.VersionRef `json:"descendant"`
	Freshness  Freshness                   `json:"freshness"`
	CreatedAt  time.Time                   `json:"createdAt"`
}

// Store owns immutable dependency edges and revision-driven invalidations.
type Store interface {
	AddDependency(context.Context, AddRequest) (Dependency, bool, error)
	Dependencies(context.Context, artifactregistry.VersionRef) ([]Dependency, error)
	Dependents(context.Context, artifactregistry.VersionRef) ([]Dependency, error)
	Freshness(context.Context, artifactregistry.VersionRef) (Freshness, error)
	Invalidations(context.Context, artifactregistry.VersionRef) ([]Invalidation, error)
}
