// Package investigation implements a bounded daemon-owned repository collection,
// independently of workflow authoring and delivery execution.
package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"darkstar/src/core/repositoryscope"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

var (
	ErrInvalidRequest = errors.New("invalid investigation request")
	ErrConflict       = errors.New("investigation revision or identity conflict")
	ErrUnavailable    = errors.New("investigation input unavailable")
	ErrClosed         = errors.New("investigation scheduler closed")
)

type ArtifactReference = statestore.InvestigationArtifactReference
type TaskInput = statestore.InvestigationTaskInput
type FrozenTask = statestore.InvestigationFrozenTask
type ProviderSelection = statestore.InvestigationProviderSelection
type UnitResult = statestore.InvestigationUnitResult
type Attempt = statestore.InvestigationAttempt

type PrepareRequest struct {
	ScopeID     string    `json:"scopeId"`
	Task        TaskInput `json:"task"`
	Concurrency int       `json:"concurrency"`
}

type View struct {
	SchedulerError string                             `json:"schedulerError,omitempty"`
	SchemaVersion  int                                `json:"schemaVersion"`
	Collection     statestore.InvestigationCollection `json:"collection"`
	Units          []statestore.InvestigationUnit     `json:"units"`
	Attempts       []Attempt                          `json:"attempts"`
}

type MissingUnit struct {
	RepositoryID string `json:"repositoryId"`
	Reason       string `json:"reason"`
}

type Execution struct {
	CollectionID    string
	ProjectID       string
	UnitID          string
	AttemptID       string
	Kind            string
	Task            FrozenTask
	Provider        ProviderSelection
	Scope           repositoryscope.AttemptView
	Findings        []UnitResult
	Missing         []MissingUnit
	CancelRequested bool
}

type Recorder interface {
	RecordPrepared(context.Context, string, string, json.RawMessage) error
	RecordHandle(context.Context, provider.AttemptHandle, string, string) error
	RecordEvent(context.Context, provider.Event) error
	RecordSubmission(context.Context, json.RawMessage) error
	RecordResult(context.Context, UnitResult) error
}

type Outcome struct {
	State  string
	Result *UnitResult
	Reason string
}

type Worker interface {
	ResolveTask(context.Context, TaskInput, string) (FrozenTask, error)
	ResolveProvider(context.Context, string) (ProviderSelection, error)
	Execute(context.Context, Execution, Recorder) Outcome
	Reconcile(context.Context, Execution, Attempt, Recorder) Outcome
}

type Options struct {
	OwnerID           string
	GlobalConcurrency int
	GlobalLimit       func() (int, error)
	PollInterval      time.Duration
	LeaseDuration     time.Duration
	Admission         sync.Locker
	OtherActive       func(context.Context) (int, error)
}
