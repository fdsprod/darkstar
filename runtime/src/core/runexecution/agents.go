package runexecution

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	manifestport "darkstar/src/ports/contextmanifest"
	"darkstar/src/ports/statestore"
)

// ErrAgentInvalidTransition classifies cancellation of an attempt that has
// already reached a terminal state other than cancelled.
var ErrAgentInvalidTransition = errors.New("agent attempt cannot be cancelled")

// AgentWorkspace is the immutable workspace identity exposed for an attempt.
// Digest is present when the value came from a frozen context manifest.
type AgentWorkspace struct {
	ID     string `json:"id"`
	Digest string `json:"digest,omitempty"`
	Access string `json:"access"`
}

// AgentExecution describes the source and requested permissions for one
// attempt. Source makes compatibility fallback data distinguishable from a
// frozen context manifest without introducing another lifecycle flag.
type AgentExecution struct {
	Source      string         `json:"source"`
	Workspace   AgentWorkspace `json:"workspace"`
	Permissions []string       `json:"permissions"`
}

// Agent is the public operational view of one provider attempt. Lifecycle
// state remains authoritative in AttemptProjection; elapsed time is derived.
type Agent struct {
	AttemptID           string                   `json:"attemptId"`
	RunID               string                   `json:"runId"`
	NodeID              string                   `json:"nodeId"`
	Provider            string                   `json:"provider"`
	Status              statestore.AttemptStatus `json:"status"`
	Execution           AgentExecution           `json:"execution"`
	LogReference        string                   `json:"logReference,omitempty"`
	ElapsedMilliseconds int64                    `json:"elapsedMilliseconds"`
	CreatedAt           time.Time                `json:"createdAt"`
	UpdatedAt           time.Time                `json:"updatedAt"`
}

// AgentList is the complete queued/running provider-attempt projection.
type AgentList struct {
	SchemaVersion int     `json:"schemaVersion"`
	Items         []Agent `json:"items"`
}

// AgentTransitionError identifies an attempt-level invalid cancellation.
type AgentTransitionError struct {
	AttemptID string
	Status    statestore.AttemptStatus
}

func (e *AgentTransitionError) Error() string {
	return fmt.Sprintf("%v: attempt %s is %s", ErrAgentInvalidTransition, e.AttemptID, e.Status)
}

func (e *AgentTransitionError) Unwrap() error { return ErrAgentInvalidTransition }

type attemptManifestReader interface {
	ManifestForAttempt(context.Context, string) (manifestport.Manifest, error)
}

// ListAgents returns only queued and executing attempts. Terminal attempts are
// still addressable through Agent so log followers can observe completion.
func (s *Service) ListAgents(ctx context.Context) (AgentList, error) {
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return AgentList{}, err
	}
	attempts := make([]statestore.AttemptProjection, 0)
	for _, run := range runs {
		owned, readErr := s.store.AttemptsForRun(ctx, run.RunID)
		if readErr != nil {
			return AgentList{}, readErr
		}
		for _, attempt := range owned {
			switch attempt.Status {
			case statestore.AttemptCreated, statestore.AttemptStarting, statestore.AttemptRunning, statestore.AttemptValidating:
				attempts = append(attempts, attempt)
			}
		}
	}
	sort.Slice(attempts, func(i, j int) bool {
		if attempts[i].Priority != attempts[j].Priority {
			return attempts[i].Priority > attempts[j].Priority
		}
		if !attempts[i].CreatedAt.Equal(attempts[j].CreatedAt) {
			return attempts[i].CreatedAt.Before(attempts[j].CreatedAt)
		}
		return attempts[i].AttemptID < attempts[j].AttemptID
	})
	items := make([]Agent, 0, len(attempts))
	for _, attempt := range attempts {
		item, err := s.agent(ctx, attempt)
		if err != nil {
			return AgentList{}, err
		}
		items = append(items, item)
	}
	return AgentList{SchemaVersion: 1, Items: items}, nil
}

// Agent returns an operational view for any persisted attempt.
func (s *Service) Agent(ctx context.Context, attemptID string) (Agent, error) {
	attempt, err := s.store.Attempt(ctx, strings.TrimSpace(attemptID))
	if err != nil {
		return Agent{}, err
	}
	return s.agent(ctx, attempt)
}

// CancelAgent cancels the run that owns the selected active attempt. The
// current execution model admits one active visit per run, so the run control
// atomically closes that attempt, its visit, and the owning run.
func (s *Service) CancelAgent(ctx context.Context, attemptID, idempotencyKey string) (Agent, error) {
	attemptID = strings.TrimSpace(attemptID)
	attempt, err := s.store.Attempt(ctx, attemptID)
	if err != nil {
		return Agent{}, err
	}
	if attempt.Status == statestore.AttemptCancelled {
		return s.agent(ctx, attempt)
	}
	if attempt.Status.Terminal() {
		return Agent{}, &AgentTransitionError{AttemptID: attemptID, Status: attempt.Status}
	}
	run, err := s.store.Run(ctx, attempt.RunID)
	if err != nil {
		return Agent{}, err
	}
	_, err = s.Cancel(ctx, ControlRequest{
		RunID: attempt.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: idempotencyKey,
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"},
	})
	if err != nil {
		return Agent{}, err
	}
	attempt, err = s.store.Attempt(ctx, attemptID)
	if err != nil {
		return Agent{}, err
	}
	return s.agent(ctx, attempt)
}

func (s *Service) agent(ctx context.Context, attempt statestore.AttemptProjection) (Agent, error) {
	execution := s.compatibilityExecution()
	if reader, ok := s.store.(attemptManifestReader); ok {
		manifest, err := reader.ManifestForAttempt(ctx, attempt.AttemptID)
		if err == nil {
			permissions := append([]string(nil), manifest.Permissions...)
			permissions = append(permissions, "workspace:"+string(manifest.Workspace.Access))
			sort.Strings(permissions)
			execution = AgentExecution{
				Source:      "context_manifest",
				Workspace:   AgentWorkspace{ID: manifest.Workspace.ID, Digest: manifest.Workspace.Digest, Access: string(manifest.Workspace.Access)},
				Permissions: permissions,
			}
		} else if !errors.Is(err, manifestport.ErrNotFound) {
			return Agent{}, fmt.Errorf("read attempt context manifest: %w", err)
		}
	}
	end := s.now().UTC().Round(0)
	if attempt.Status.Terminal() {
		end = attempt.UpdatedAt
	}
	elapsed := end.Sub(attempt.CreatedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	return Agent{
		AttemptID: attempt.AttemptID, RunID: attempt.RunID, NodeID: attempt.NodeID,
		Provider: attempt.Provider, Status: attempt.Status, Execution: execution,
		LogReference: attempt.LogReference, ElapsedMilliseconds: elapsed,
		CreatedAt: attempt.CreatedAt, UpdatedAt: attempt.UpdatedAt,
	}, nil
}

func (s *Service) compatibilityExecution() AgentExecution {
	s.mu.Lock()
	workspace := s.workspace
	s.mu.Unlock()
	return AgentExecution{
		Source:      "compatibility_policy",
		Workspace:   AgentWorkspace{ID: workspace, Access: "read_only"},
		Permissions: []string{"commands:deny", "files:deny", "network:denied", "tools:deny", "workspace:read_only"},
	}
}
