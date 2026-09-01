// Package artifactbinding defines versioned attachment histories for artifacts.
package artifactbinding

import (
	"context"
	"errors"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

var (
	// ErrNotFound classifies a missing binding or binding version.
	ErrNotFound = errors.New("artifact binding not found")
	// ErrConflict means an idempotency key or binding identity was reused with
	// different facts.
	ErrConflict = errors.New("artifact binding conflict")
	// ErrStateConflict means the requested bind/unbind transition is illegal.
	ErrStateConflict = errors.New("artifact binding state conflict")
)

// State is the closed lifecycle for one logical binding.
type State string

const (
	StateBound   State = "bound"
	StateUnbound State = "unbound"
)

// TargetKind is the closed set of entities supported by DS-073.
type TargetKind string

const (
	TargetProject             TargetKind = "project"
	TargetWork                TargetKind = "work"
	TargetRun                 TargetKind = "run"
	TargetNode                TargetKind = "node"
	TargetCheckpoint          TargetKind = "checkpoint"
	TargetDecision            TargetKind = "decision"
	TargetStory               TargetKind = "story"
	TargetImplementationPoint TargetKind = "implementation_point"
)

// Target identifies exactly one bindable entity.
type Target struct {
	Kind TargetKind `json:"kind"`
	ID   string     `json:"id"`
}

// Version is one immutable snapshot in a binding history.
type Version struct {
	BindingID string                      `json:"bindingId"`
	Version   uint64                      `json:"version"`
	State     State                       `json:"state"`
	Artifact  artifactregistry.VersionRef `json:"artifact"`
	Target    Target                      `json:"target"`
	CreatedAt time.Time                   `json:"createdAt"`
}

// BindRequest creates or reactivates one logical binding. Reactivation keeps
// the artifact identity and target stable while allowing a newer exact version.
type BindRequest struct {
	BindingID      string
	IdempotencyKey string
	Artifact       artifactregistry.VersionRef
	Target         Target
	CreatedAt      time.Time
}

// UnbindRequest deactivates one logical binding without discarding its history.
type UnbindRequest struct {
	BindingID      string
	IdempotencyKey string
	CreatedAt      time.Time
}

// Store appends binding transitions and reads exact or current state.
type Store interface {
	Bind(context.Context, BindRequest) (Version, bool, error)
	Unbind(context.Context, UnbindRequest) (Version, bool, error)
	BindingVersion(context.Context, string, uint64) (Version, error)
	LatestBinding(context.Context, string) (Version, error)
	BindingVersions(context.Context, string) ([]Version, error)
	ActiveBindings(context.Context, Target) ([]Version, error)
}
