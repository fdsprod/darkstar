package runexecution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

var (
	ErrAgentInvalidRequest    = errors.New("invalid agent control request")
	ErrAgentVersionConflict   = errors.New("agent resource version conflict")
	ErrAgentCommandInProgress = errors.New("agent cancellation is still being recovered")
)

type AgentAction string

const AgentActionCancel AgentAction = "cancel"

// AgentWorkspace is the immutable workspace identity exposed for an attempt.
// Digest is present when the value came from a frozen context manifest.
type AgentWorkspace struct {
	ID     string                       `json:"id"`
	Digest string                       `json:"digest,omitempty"`
	Access manifestport.WorkspaceAccess `json:"access"`
}

type AgentExecutionSource string

const (
	AgentExecutionContextManifest AgentExecutionSource = "context_manifest"
	AgentExecutionCompatibility   AgentExecutionSource = "compatibility_policy"
)

// AgentExecution describes the source and requested permissions for one
// attempt. Source makes compatibility fallback data distinguishable from a
// frozen context manifest without introducing another lifecycle flag.
type AgentExecution struct {
	Source      AgentExecutionSource `json:"source"`
	Workspace   AgentWorkspace       `json:"workspace"`
	Permissions []string             `json:"permissions"`
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
	AllowedActions      []AgentAction            `json:"allowedActions"`
	ResourceVersion     uint64                   `json:"resourceVersion"`
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

type AgentVersionConflictError struct {
	AttemptID string
	Expected  uint64
	Current   uint64
}

func (e *AgentVersionConflictError) Error() string {
	return fmt.Sprintf("%v for %s: expected %d, current %d", ErrAgentVersionConflict, e.AttemptID, e.Expected, e.Current)
}

func (e *AgentVersionConflictError) Unwrap() error { return ErrAgentVersionConflict }

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
func (s *Service) CancelAgent(ctx context.Context, attemptID string, expectedResourceVersion uint64, idempotencyKey string) (Agent, error) {
	attemptID = strings.TrimSpace(attemptID)
	if expectedResourceVersion == 0 || strings.TrimSpace(idempotencyKey) != idempotencyKey || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return Agent{}, ErrAgentInvalidRequest
	}
	attempt, err := s.store.Attempt(ctx, attemptID)
	if err != nil {
		return Agent{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", attemptID, expectedResourceVersion))))
	command, reused, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: "agents.cancel", IdempotencyKey: idempotencyKey, RequestDigest: digest, CreatedAt: s.now().UTC().Round(0)})
	if err != nil {
		return Agent{}, err
	}
	if reused && command.Status == "completed" {
		return decodeAgentCancelResponse(command.Response)
	}
	if reused {
		return Agent{}, ErrAgentCommandInProgress
	}
	if attempt.ResourceVersion != expectedResourceVersion {
		failure := &AgentVersionConflictError{AttemptID: attemptID, Expected: expectedResourceVersion, Current: attempt.ResourceVersion}
		return Agent{}, s.completeAgentCancelFailure(ctx, idempotencyKey, "version_conflict", failure, attempt.ResourceVersion)
	}
	if attempt.Status == statestore.AttemptCancelled {
		value, viewErr := s.agent(ctx, attempt)
		if viewErr != nil {
			return Agent{}, viewErr
		}
		return value, s.completeAgentCancelSuccess(ctx, idempotencyKey, value)
	}
	if attempt.Status.Terminal() {
		failure := &AgentTransitionError{AttemptID: attemptID, Status: attempt.Status}
		return Agent{}, s.completeAgentCancelFailure(ctx, idempotencyKey, "invalid_transition", failure, attempt.ResourceVersion)
	}
	run, err := s.store.Run(ctx, attempt.RunID)
	if err != nil {
		return Agent{}, s.completeAgentCancelFailure(ctx, idempotencyKey, "cancel_failed", err, attempt.ResourceVersion)
	}
	_, err = s.Cancel(ctx, ControlRequest{
		RunID: attempt.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: idempotencyKey,
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"},
	})
	if err != nil {
		if errors.Is(err, ErrCommandInProgress) {
			return Agent{}, ErrAgentCommandInProgress
		}
		return Agent{}, s.completeAgentCancelFailure(ctx, idempotencyKey, "cancel_failed", err, attempt.ResourceVersion)
	}
	attempt, err = s.store.Attempt(ctx, attemptID)
	if err != nil {
		return Agent{}, err
	}
	value, err := s.agent(ctx, attempt)
	if err != nil {
		return Agent{}, err
	}
	return value, s.completeAgentCancelSuccess(ctx, idempotencyKey, value)
}

type agentCancelCommandResponse struct {
	Result  *Agent              `json:"result,omitempty"`
	Failure *agentCancelFailure `json:"failure,omitempty"`
}

type agentCancelFailure struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	AttemptID string `json:"attemptId,omitempty"`
	Expected  uint64 `json:"expected,omitempty"`
	Current   uint64 `json:"current,omitempty"`
}

func (s *Service) completeAgentCancelSuccess(ctx context.Context, key string, value Agent) error {
	encoded, _ := json.Marshal(agentCancelCommandResponse{Result: &value})
	_, err := s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: "agents.cancel", IdempotencyKey: key, ResponseStatus: 200, Response: encoded, CompletedAt: s.now().UTC().Round(0)})
	return err
}

func (s *Service) completeAgentCancelFailure(ctx context.Context, key, kind string, cause error, current uint64) error {
	failure := &agentCancelFailure{Kind: kind, Message: cause.Error(), Current: current}
	var versionConflict *AgentVersionConflictError
	if errors.As(cause, &versionConflict) {
		failure.AttemptID, failure.Expected = versionConflict.AttemptID, versionConflict.Expected
	}
	encoded, _ := json.Marshal(agentCancelCommandResponse{Failure: failure})
	if _, err := s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: "agents.cancel", IdempotencyKey: key, ResponseStatus: 409, Response: encoded, CompletedAt: s.now().UTC().Round(0)}); err != nil {
		return err
	}
	return cause
}

func decodeAgentCancelResponse(content json.RawMessage) (Agent, error) {
	var response agentCancelCommandResponse
	if err := json.Unmarshal(content, &response); err != nil || (response.Result == nil) == (response.Failure == nil) {
		return Agent{}, errors.New("invalid persisted agent cancellation response")
	}
	if response.Result != nil {
		return *response.Result, nil
	}
	switch response.Failure.Kind {
	case "version_conflict":
		return Agent{}, &AgentVersionConflictError{AttemptID: response.Failure.AttemptID, Current: response.Failure.Current, Expected: response.Failure.Expected}
	case "invalid_transition":
		return Agent{}, fmt.Errorf("%w: %s", ErrAgentInvalidTransition, response.Failure.Message)
	default:
		return Agent{}, errors.New(response.Failure.Message)
	}
}

func (s *Service) agent(ctx context.Context, attempt statestore.AttemptProjection) (Agent, error) {
	run, err := s.store.Run(ctx, attempt.RunID)
	if err != nil {
		return Agent{}, fmt.Errorf("read agent run: %w", err)
	}
	execution := s.compatibilityExecution()
	if reader, ok := s.store.(attemptManifestReader); ok {
		manifest, err := reader.ManifestForAttempt(ctx, attempt.AttemptID)
		if err == nil {
			permissions := append([]string(nil), manifest.Permissions...)
			permissions = append(permissions, "workspace:"+string(manifest.Workspace.Access))
			sort.Strings(permissions)
			execution = AgentExecution{
				Source:      AgentExecutionContextManifest,
				Workspace:   AgentWorkspace{ID: manifest.Workspace.ID, Digest: manifest.Workspace.Digest, Access: manifest.Workspace.Access},
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
		AllowedActions: allowedAgentActions(attempt.Status, run.Status), ResourceVersion: attempt.ResourceVersion,
		CreatedAt: attempt.CreatedAt, UpdatedAt: attempt.UpdatedAt,
	}, nil
}

func (s *Service) compatibilityExecution() AgentExecution {
	s.mu.Lock()
	workspace := s.workspace
	s.mu.Unlock()
	return AgentExecution{
		Source:      AgentExecutionCompatibility,
		Workspace:   AgentWorkspace{ID: workspace, Access: manifestport.WorkspaceReadOnly},
		Permissions: []string{"commands:deny", "files:deny", "network:denied", "tools:deny", "workspace:read_only"},
	}
}

func allowedAgentActions(attempt statestore.AttemptStatus, run statestore.RunStatus) []AgentAction {
	if attempt.Terminal() {
		return []AgentAction{}
	}
	switch run {
	case statestore.RunDraft, statestore.RunReady, statestore.RunQueued, statestore.RunRunning, statestore.RunWaiting, statestore.RunBlocked, statestore.RunFailed:
		return []AgentAction{AgentActionCancel}
	default:
		return []AgentAction{}
	}
}
