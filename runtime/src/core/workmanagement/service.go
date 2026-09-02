// Package workmanagement owns project and work-item command/query behavior.
package workmanagement

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/ports/statestore"
)

const (
	projectRegisterScope = "projects.register"
	workCreateScope      = "work.create"
	workImportScope      = "work.import"
)

var (
	// ErrCommandInProgress means durable command evidence exists but its response is not closed yet.
	ErrCommandInProgress = errors.New("work command is still being recovered")
	// ErrInvalidRequest classifies malformed project or work input.
	ErrInvalidRequest = errors.New("invalid project or work request")
)

// ProjectRegistration identifies one repository source without persisting its location.
type ProjectRegistration struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// CreateWorkRequest is authored work under one registered project.
type CreateWorkRequest struct {
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
	Priority  int    `json:"priority,omitempty"`
}

// ImportWorkRequest is externally sourced work. SourceReference is fingerprinted,
// while Title is the local display value and may default to SourceReference.
type ImportWorkRequest struct {
	ProjectID       string `json:"projectId"`
	SourceReference string `json:"sourceReference"`
	Title           string `json:"title,omitempty"`
	Priority        int    `json:"priority,omitempty"`
}

// ProjectView combines a project with its directly owned work items.
type ProjectView struct {
	SchemaVersion int                             `json:"schemaVersion"`
	Project       statestore.ProjectProjection    `json:"project"`
	WorkItems     []statestore.WorkItemProjection `json:"workItems"`
}

// WorkView combines one work item with its current run and story projections.
type WorkView struct {
	SchemaVersion int                           `json:"schemaVersion"`
	Work          statestore.WorkItemProjection `json:"work"`
	Runs          []statestore.RunProjection    `json:"runs"`
	Stories       []statestore.StoryProjection  `json:"stories"`
}

// Service owns project and work-item public commands.
type Service struct {
	store statestore.Store
	now   func() time.Time
}

// New constructs the service over the authoritative event store.
func New(store statestore.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("work management requires state")
	}
	return &Service{store: store, now: time.Now}, nil
}

// RegisterProject idempotently registers one repository source.
func (s *Service) RegisterProject(ctx context.Context, request ProjectRegistration, idempotencyKey string) (statestore.ProjectProjection, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.Source = strings.TrimSpace(request.Source)
	if request.Name == "" || request.Source == "" {
		return statestore.ProjectProjection{}, fmt.Errorf("%w: project name and source are required", ErrInvalidRequest)
	}
	command, reused, err := s.begin(ctx, projectRegisterScope, idempotencyKey, request)
	if err != nil {
		return statestore.ProjectProjection{}, err
	}
	if reused && command.Status == "completed" {
		var value statestore.ProjectProjection
		if err := json.Unmarshal(command.Response, &value); err != nil {
			return statestore.ProjectProjection{}, fmt.Errorf("decode replayed project response: %w", err)
		}
		return value, nil
	}

	projectID := identity.Deterministic("project_", projectRegisterScope+"\x00"+idempotencyKey)
	if reused {
		value, getErr := s.store.Project(ctx, projectID)
		if getErr != nil {
			return statestore.ProjectProjection{}, ErrCommandInProgress
		}
		return value, s.complete(ctx, projectRegisterScope, idempotencyKey, httpCreated, value, nil)
	}

	sourceHash := digest(request.Source)
	projects, err := s.store.Projects(ctx)
	if err != nil {
		return statestore.ProjectProjection{}, err
	}
	for _, value := range projects {
		if value.SourceHash == sourceHash {
			return value, s.complete(ctx, projectRegisterScope, idempotencyKey, httpOK, value, nil)
		}
	}

	now := s.now().UTC().Round(0)
	events, err := s.store.Append(ctx, pendingEvent("project.created", statestore.AggregateProject, projectID, projectID, idempotencyKey, now, map[string]any{
		"name": request.Name, "sourceHash": sourceHash,
	}))
	if err != nil {
		return statestore.ProjectProjection{}, err
	}
	value, err := s.store.Project(ctx, projectID)
	if err != nil {
		return statestore.ProjectProjection{}, err
	}
	return value, s.complete(ctx, projectRegisterScope, idempotencyKey, httpCreated, value, events)
}

// Projects returns the deterministic project list projection.
func (s *Service) Projects(ctx context.Context) ([]statestore.ProjectProjection, error) {
	return s.store.Projects(ctx)
}

// Project returns one project and its work items.
func (s *Service) Project(ctx context.Context, projectID string) (ProjectView, error) {
	project, err := s.store.Project(ctx, projectID)
	if err != nil {
		return ProjectView{}, err
	}
	workItems, err := s.store.WorkItemsForProject(ctx, projectID)
	if err != nil {
		return ProjectView{}, err
	}
	return ProjectView{SchemaVersion: 1, Project: project, WorkItems: workItems}, nil
}

// CreateWork creates authored work from its title.
func (s *Service) CreateWork(ctx context.Context, request CreateWorkRequest, idempotencyKey string) (statestore.WorkItemProjection, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.Title = strings.TrimSpace(request.Title)
	if request.ProjectID == "" || request.Title == "" || request.Priority < 0 {
		return statestore.WorkItemProjection{}, fmt.Errorf("%w: projectId, title, and a non-negative priority are required", ErrInvalidRequest)
	}
	return s.createWork(ctx, workCreateScope, request.ProjectID, request.Title, request.Title, request.Priority, idempotencyKey, request)
}

// ImportWork creates externally sourced work without storing provider-specific source data.
func (s *Service) ImportWork(ctx context.Context, request ImportWorkRequest, idempotencyKey string) (statestore.WorkItemProjection, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.SourceReference = strings.TrimSpace(request.SourceReference)
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" {
		request.Title = request.SourceReference
	}
	if request.ProjectID == "" || request.SourceReference == "" || request.Priority < 0 {
		return statestore.WorkItemProjection{}, fmt.Errorf("%w: projectId, sourceReference, and a non-negative priority are required", ErrInvalidRequest)
	}
	return s.createWork(ctx, workImportScope, request.ProjectID, request.Title, request.SourceReference, request.Priority, idempotencyKey, request)
}

// WorkItems returns all work, or only work owned by one exact project.
func (s *Service) WorkItems(ctx context.Context, projectID string) ([]statestore.WorkItemProjection, error) {
	if projectID == "" {
		return s.store.WorkItems(ctx)
	}
	if _, err := s.store.Project(ctx, projectID); err != nil {
		return nil, err
	}
	return s.store.WorkItemsForProject(ctx, projectID)
}

// WorkItem returns one work item and its directly owned runs and stories.
func (s *Service) WorkItem(ctx context.Context, workItemID string) (WorkView, error) {
	work, err := s.store.WorkItem(ctx, workItemID)
	if err != nil {
		return WorkView{}, err
	}
	runs, err := s.store.RunsForWorkItem(ctx, workItemID)
	if err != nil {
		return WorkView{}, err
	}
	stories, err := s.store.StoriesForWorkItem(ctx, workItemID)
	if err != nil {
		return WorkView{}, err
	}
	return WorkView{SchemaVersion: 1, Work: work, Runs: runs, Stories: stories}, nil
}

func (s *Service) createWork(ctx context.Context, scope, projectID, title, source string, priority int, idempotencyKey string, request any) (statestore.WorkItemProjection, error) {
	project, err := s.store.Project(ctx, projectID)
	if err != nil {
		return statestore.WorkItemProjection{}, err
	}
	if project.Status != statestore.ProjectActive {
		return statestore.WorkItemProjection{}, fmt.Errorf("%w: project %s is archived", ErrInvalidRequest, projectID)
	}
	command, reused, err := s.begin(ctx, scope, idempotencyKey, request)
	if err != nil {
		return statestore.WorkItemProjection{}, err
	}
	if reused && command.Status == "completed" {
		var value statestore.WorkItemProjection
		if err := json.Unmarshal(command.Response, &value); err != nil {
			return statestore.WorkItemProjection{}, fmt.Errorf("decode replayed work response: %w", err)
		}
		return value, nil
	}
	workID := identity.Deterministic("work_", scope+"\x00"+idempotencyKey)
	if reused {
		value, getErr := s.store.WorkItem(ctx, workID)
		if getErr != nil {
			return statestore.WorkItemProjection{}, ErrCommandInProgress
		}
		return value, s.complete(ctx, scope, idempotencyKey, httpCreated, value, nil)
	}

	now := s.now().UTC().Round(0)
	events, err := s.store.Append(ctx, pendingEvent("work.created", statestore.AggregateWork, workID, workID, idempotencyKey, now, map[string]any{
		"projectId": projectID, "title": title, "sourceHash": digest(source), "priority": priority,
	}))
	if err != nil {
		return statestore.WorkItemProjection{}, err
	}
	value, err := s.store.WorkItem(ctx, workID)
	if err != nil {
		return statestore.WorkItemProjection{}, err
	}
	return value, s.complete(ctx, scope, idempotencyKey, httpCreated, value, events)
}

const (
	httpOK      = 200
	httpCreated = 201
)

func (s *Service) begin(ctx context.Context, scope, key string, request any) (statestore.CommandEvidence, bool, error) {
	if strings.TrimSpace(key) != key || len(key) < 8 || len(key) > 128 {
		return statestore.CommandEvidence{}, false, fmt.Errorf("%w: idempotency key must be between 8 and 128 bytes without surrounding whitespace", ErrInvalidRequest)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return statestore.CommandEvidence{}, false, err
	}
	return s.store.BeginCommand(ctx, statestore.BeginCommandRequest{
		Scope: scope, IdempotencyKey: key, RequestDigest: digest(string(encoded)), CreatedAt: s.now().UTC().Round(0),
	})
}

func (s *Service) complete(ctx context.Context, scope, key string, status int, value any, events []statestore.Event) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	request := statestore.CompleteCommandRequest{Scope: scope, IdempotencyKey: key, ResponseStatus: status, Response: encoded, CompletedAt: s.now().UTC().Round(0)}
	if len(events) != 0 {
		first, last := events[0].GlobalPosition, events[len(events)-1].GlobalPosition
		request.FirstEventPosition, request.LastEventPosition = &first, &last
	}
	_, err = s.store.CompleteCommand(ctx, request)
	return err
}

func pendingEvent(kind string, aggregateType statestore.AggregateType, aggregateID, correlationID, commandID string, occurredAt time.Time, data any) statestore.PendingEvent {
	encoded, _ := json.Marshal(data)
	return statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: aggregateType, AggregateID: aggregateID,
		ExpectedRevision: 0, Kind: kind, OccurredAt: occurredAt, CorrelationID: correlationID, CommandID: commandID,
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "cli"}, Data: encoded, Metadata: json.RawMessage(`{}`)}
}

func digest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
