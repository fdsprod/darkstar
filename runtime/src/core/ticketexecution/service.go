// Package ticketexecution owns explicit source-version approval and local
// execution lineage. Provider business state is observation, never run authority.
package ticketexecution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"darkstar/src/core/backlog"
	"darkstar/src/core/identity"
	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
	"darkstar/src/ports/workspace"
)

type Store interface {
	statestore.TicketExecutionStore
	statestore.BacklogStore
	Project(context.Context, string) (statestore.ProjectProjection, error)
	WorkItem(context.Context, string) (statestore.WorkItemProjection, error)
	WorkItems(context.Context) ([]statestore.WorkItemProjection, error)
	WorkItemsForProject(context.Context, string) ([]statestore.WorkItemProjection, error)
	RunsForWorkItem(context.Context, string) ([]statestore.RunProjection, error)
}

type Options struct {
	Now               func() time.Time
	Workspaces        workspace.Provisioner
	MaxObservationAge time.Duration
}

type Service struct {
	store    Store
	resolver backlog.Resolver
	options  Options
}

type AdmissionRequest struct {
	ProjectID       string
	BindingRevision uint64
	ObservationID   string
	RoutingIntent   *statestore.WorkRoutingIntent
}

type RebindRequest struct {
	WorkID                                   string
	ExpectedLineageRevision, BindingRevision uint64
	ObservationID                            string
}

type SourceAssessmentState string

const (
	SourceReady          SourceAssessmentState = "ready"
	SourceActionRequired SourceAssessmentState = "action_required"
	SourceUnresolved     SourceAssessmentState = "unresolved_source"
)

type SourceAssessment struct {
	State   SourceAssessmentState
	Reasons []string
}

type WorkSourceView struct {
	Work                          statestore.WorkItemProjection
	Lineages                      []statestore.SourceLineage
	Lineage                       *statestore.SourceLineage
	Approval                      *statestore.TicketAdmission
	Approved, Current             *tracker.Ticket
	CurrentObservationID          string
	Assessment                    SourceAssessment
	Runs                          []statestore.RunProjection
	LocalActivity, LastRunOutcome string
	ExternalAcceptance            tracker.Knowledge[string]
}

type Result struct {
	Admission statestore.TicketAdmission
	Work      statestore.WorkItemProjection
	Source    WorkSourceView
}

func New(store Store, resolver backlog.Resolver, options Options) (*Service, error) {
	if store == nil {
		return nil, fail(ports.FailureInvalidRequest, "source execution requires durable storage")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxObservationAge == 0 {
		options.MaxObservationAge = 5 * time.Minute
	}
	if options.MaxObservationAge < time.Second {
		return nil, fail(ports.FailureInvalidRequest, "source observation age must be positive")
	}
	return &Service{store: store, resolver: resolver, options: options}, nil
}

func (s *Service) Admit(ctx context.Context, request AdmissionRequest, key string) (Result, error) {
	if request.ProjectID == "" || request.BindingRevision == 0 || request.ObservationID == "" || !validKey(key) {
		return Result{}, fail(ports.FailureInvalidRequest, "source admission requires exact project, binding, observation and idempotency key")
	}
	observation, err := s.store.BacklogObservation(ctx, request.ObservationID)
	if err != nil {
		return Result{}, err
	}
	ticket, err := trackercontract.DecodeTicket(observation.Ticket)
	if err != nil {
		return Result{}, err
	}
	routing := statestore.WorkRoutingIntent{Mode: statestore.WorkRoutingAutomatic}
	if request.RoutingIntent != nil {
		routing = *request.RoutingIntent
	}
	if (routing.Mode != statestore.WorkRoutingAutomatic && routing.Mode != statestore.WorkRoutingOverride) || (routing.Mode == statestore.WorkRoutingOverride && routing.WorkflowID == "") || (routing.Mode == statestore.WorkRoutingAutomatic && (routing.WorkflowID != "" || routing.WorkflowVersion != "" || routing.EntryNodeID != "" || len(routing.TerminalNodeIDs) > 0)) {
		return Result{}, fail(ports.FailureInvalidRequest, "source admission routing intent is invalid")
	}
	work := identity.Deterministic("work_", "source-ticket\x00"+request.ProjectID+"\x00"+observation.TicketKey)
	now := s.options.Now().UTC()
	priority := 0
	if observed, ok := ticket.Priority.(tracker.Known[tracker.NamedID]); ok {
		if value, err := strconv.Atoi(observed.Value.ID); err == nil && value >= 0 {
			priority = value
		}
	}
	data, err := json.Marshal(map[string]any{"projectId": request.ProjectID, "title": ticket.Title, "details": ticket.Description, "sourceHash": observation.ContentDigest, "priority": priority, "routingIntent": routing, "evidence": []string{observation.EvidenceRef, observation.ID}})
	if err != nil {
		return Result{}, err
	}
	mutation := statestore.SourceAdmissionMutation{ProjectID: request.ProjectID, BindingRevision: request.BindingRevision, ObservationID: request.ObservationID, WorkID: work, IdempotencyKey: key, RequestDigest: digest(struct {
		Action  string
		Request AdmissionRequest
	}{"admit", request}), AdmissionID: identity.Deterministic("operation_", "source-approval\x00"+key), Actor: "local-user", ApprovedAt: now, ObservedNotBefore: now.Add(-s.options.MaxObservationAge),
		NewWork: statestore.PendingEvent{SchemaVersion: 1, ID: identity.Deterministic("event_", "source-admission\x00"+key), AggregateType: statestore.AggregateWork, AggregateID: work, Kind: "work.created", OccurredAt: now, CorrelationID: work, CommandID: "source-admission:" + key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, Data: data, Metadata: json.RawMessage(`{}`)},
	}
	admission, err := s.store.AdmitSourceTicket(ctx, mutation)
	if err != nil {
		return Result{}, err
	}
	return s.result(ctx, admission)
}

func (s *Service) Approve(ctx context.Context, work, observation, key string) (Result, error) {
	if !validKey(key) || observation == "" {
		return Result{}, fail(ports.FailureInvalidRequest, "source approval requires exact observation and idempotency key")
	}
	lineage, err := s.store.WorkTicketLineage(ctx, work)
	if err != nil {
		return Result{}, err
	}
	request := statestore.SourceAdmissionMutation{ProjectID: lineage.ProjectID, BindingRevision: lineage.BindingRevision, ExistingWorkID: work, WorkID: work, ObservationID: observation, IdempotencyKey: key, RequestDigest: digest(struct{ Action, WorkID, ObservationID string }{"approve", work, observation}), AdmissionID: identity.Deterministic("operation_", "source-approval\x00"+key), Actor: "local-user", ApprovedAt: s.options.Now().UTC(), ObservedNotBefore: s.options.Now().UTC().Add(-s.options.MaxObservationAge)}
	admission, err := s.store.AdmitSourceTicket(ctx, request)
	if err != nil {
		return Result{}, err
	}
	return s.result(ctx, admission)
}

func (s *Service) Rebind(ctx context.Context, request RebindRequest, key string) (Result, error) {
	if !validKey(key) || request.WorkID == "" || request.ExpectedLineageRevision == 0 || request.BindingRevision == 0 || request.ObservationID == "" {
		return Result{}, fail(ports.FailureInvalidRequest, "source rebind requires exact lineage, destination observation and idempotency key")
	}
	admission, err := s.store.RebindSourceTicket(ctx, statestore.SourceRebindMutation{WorkID: request.WorkID, ExpectedLineageRevision: request.ExpectedLineageRevision, BindingRevision: request.BindingRevision, ObservationID: request.ObservationID, IdempotencyKey: key, RequestDigest: digest(struct {
		Action  string
		Request RebindRequest
	}{"rebind", request}), AdmissionID: identity.Deterministic("operation_", "source-approval\x00"+key), Actor: "local-user", ApprovedAt: s.options.Now().UTC(), ObservedNotBefore: s.options.Now().UTC().Add(-s.options.MaxObservationAge)})
	if err != nil {
		return Result{}, err
	}
	return s.result(ctx, admission)
}

func (s *Service) result(ctx context.Context, admission statestore.TicketAdmission) (Result, error) {
	if s.options.Workspaces != nil {
		if err := s.options.Workspaces.Ensure(ctx, admission.WorkID); err != nil {
			return Result{}, fail(ports.FailureUnavailable, "source work workspace could not be prepared; retry the same request")
		}
	}
	view, err := s.Inspect(ctx, admission.WorkID)
	if err != nil {
		return Result{}, err
	}
	return Result{Admission: admission, Work: view.Work, Source: view}, nil
}

func (s *Service) Inspect(ctx context.Context, work string) (WorkSourceView, error) {
	value, err := s.store.WorkItem(ctx, work)
	if err != nil {
		return WorkSourceView{}, err
	}
	result := WorkSourceView{Work: value, Assessment: SourceAssessment{State: "unresolved_source", Reasons: []string{"legacy source provenance is unresolved"}}, Lineages: []statestore.SourceLineage{}, Runs: []statestore.RunProjection{}, LocalActivity: "idle", LastRunOutcome: "unobserved", ExternalAcceptance: tracker.Unknown[string]{Reason: "external acceptance or handoff has not been observed"}}
	result.Runs, err = s.store.RunsForWorkItem(ctx, work)
	if err != nil {
		return result, err
	}
	var latestActivity, latestOutcome time.Time
	for _, run := range result.Runs {
		switch run.Status {
		case statestore.RunCompleted, statestore.RunCancelled, statestore.RunFailed:
			if !run.UpdatedAt.Before(latestOutcome) {
				result.LastRunOutcome, latestOutcome = string(run.Status), run.UpdatedAt
			}
		default:
			if !run.CreatedAt.Before(latestActivity) {
				result.LocalActivity, latestActivity = string(run.Status), run.CreatedAt
			}
		}
	}
	lineage, err := s.store.WorkTicketLineage(ctx, work)
	if missing(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Lineage = &lineage
	result.Lineages, err = s.store.WorkTicketLineages(ctx, work)
	if err != nil {
		return result, err
	}
	result.Assessment = SourceAssessment{State: "action_required", Reasons: []string{}}
	approval, err := s.store.LatestTicketAdmission(ctx, work)
	if err == nil {
		result.Approval = &approval
		approved, err := s.store.BacklogObservation(ctx, approval.ObservationID)
		if err != nil {
			return result, err
		}
		ticket, err := trackercontract.DecodeTicket(approved.Ticket)
		if err != nil {
			return result, err
		}
		result.Approved = &ticket
	} else if !missing(err) {
		return result, err
	}
	current, err := s.store.CurrentSourceObservation(ctx, work)
	if missing(err) {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source has not been observed for this lineage")
		return result, nil
	}
	if err != nil {
		return result, err
	}
	ticket, err := trackercontract.DecodeTicket(current.Observation.Ticket)
	if err != nil {
		return result, err
	}
	result.Current, result.CurrentObservationID = &ticket, current.ObservationID
	if result.Approval == nil {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source version requires explicit approval")
	} else if result.Approval.ObservationID != current.ObservationID {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source changed after approval; active run inputs remain frozen")
	}
	if current.State == statestore.BacklogMissing {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source is missing; cancellation has not been assumed")
	}
	if current.State == statestore.BacklogInaccessible {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source access is unavailable; reconcile before new execution")
	}
	if s.options.Now().Sub(current.CheckedAt) > s.options.MaxObservationAge {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source observation is stale; refresh before new execution")
	}
	if archived, ok := ticket.Archived.(tracker.Known[bool]); ok && archived.Value {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source is archived; human action is required")
	}
	if cancelled(ticket) {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source cancellation observed; local cancellation has not been assumed")
	}
	binding, err := s.binding(ctx, lineage)
	if err != nil {
		return result, err
	}
	if placement, ok := ticket.Placement.(tracker.Known[tracker.Scope]); ok && placement.Value != backlog.Scope(binding) {
		result.Assessment.Reasons = append(result.Assessment.Reasons, "source moved outside the pinned scope; explicit rebind requires settled execution")
	}
	if len(result.Assessment.Reasons) == 0 {
		result.Assessment.State = "ready"
	}
	return result, nil
}

func (s *Service) ListWorkSourceViews(ctx context.Context, project string) ([]WorkSourceView, error) {
	var works []statestore.WorkItemProjection
	var err error
	if project == "" {
		works, err = s.store.WorkItems(ctx)
	} else {
		works, err = s.store.WorkItemsForProject(ctx, project)
	}
	if err != nil {
		return nil, err
	}
	values := make([]WorkSourceView, 0, len(works))
	for _, work := range works {
		view, err := s.Inspect(ctx, work.WorkItemID)
		if err != nil {
			return nil, err
		}
		values = append(values, view)
	}
	return values, nil
}

func (s *Service) TicketView(ctx context.Context, project, observation string) ([]WorkSourceView, error) {
	observed, err := s.store.BacklogObservation(ctx, observation)
	if err != nil {
		return nil, err
	}
	views, err := s.ListWorkSourceViews(ctx, project)
	if err != nil {
		return nil, err
	}
	values := []WorkSourceView{}
	for _, view := range views {
		for _, lineage := range view.Lineages {
			if lineage.Ref == observed.Ref {
				values = append(values, view)
				break
			}
		}
	}
	return values, nil
}

func (s *Service) binding(ctx context.Context, lineage statestore.SourceLineage) (statestore.BacklogBinding, error) {
	values, err := s.store.BacklogBindingHistory(ctx, lineage.ProjectID)
	if err != nil {
		return statestore.BacklogBinding{}, err
	}
	for _, value := range values {
		if value.Revision == lineage.BindingRevision {
			return value, nil
		}
	}
	return statestore.BacklogBinding{}, fail(ports.FailureProtocolDrift, "immutable work source binding is missing")
}

func (s *Service) RefreshSource(ctx context.Context, work string) (WorkSourceView, error) {
	if s.resolver == nil {
		return WorkSourceView{}, fail(ports.FailureUnavailable, "source resolver is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lineage, err := s.store.WorkTicketLineage(ctx, work)
	if err != nil {
		return WorkSourceView{}, err
	}
	binding, err := s.binding(ctx, lineage)
	if err != nil {
		return WorkSourceView{}, err
	}
	resolved, err := s.resolver.Resolve(ctx, binding)
	if err != nil || resolved.Source == nil {
		return WorkSourceView{}, fail(ports.FailureUnavailable, "pinned source connection cannot be resolved")
	}
	manifest, err := resolved.Source.Discover(ctx, resolved.Config)
	if err != nil {
		return WorkSourceView{}, s.recordAccessLoss(ctx, lineage, tracker.Pin{AdapterConfigPin: resolved.Config}, err)
	}
	if manifest.Scope != backlog.Scope(binding) || manifest.Pin.AdapterConfigPin != resolved.Config || manifest.Pin.BindingRevision != strconv.FormatUint(binding.Revision, 10) || trackercontract.ValidatePin(manifest.Pin, manifest.Pin) != nil || manifest.ObservedAt.IsZero() || manifest.ObservedAt.After(s.options.Now().Add(time.Minute)) || manifest.EvidenceRef == "" {
		return WorkSourceView{}, fail(ports.FailureProtocolDrift, "source discovery differs from pinned work lineage")
	}
	previous, previousErr := s.store.CurrentSourceObservation(ctx, work)
	if previousErr != nil && !missing(previousErr) {
		return WorkSourceView{}, previousErr
	}
	result, err := resolved.Source.Read(ctx, worksource.ReadTicketRequest{Pin: manifest.Pin, Ref: lineage.Ref, KnownRevision: previous.Observation.NativeRevision})
	if err != nil {
		return WorkSourceView{}, s.recordAccessLoss(ctx, lineage, manifest.Pin, err)
	}
	check := statestore.SourceCheckMutation{WorkID: work, ExpectedLineageRevision: lineage.Revision, CheckedAt: s.options.Now().UTC(), Pin: manifest.Pin}
	switch value := result.(type) {
	case tracker.Found:
		if value.Ticket.Ref != lineage.Ref {
			return WorkSourceView{}, fail(ports.FailureProtocolDrift, "exact source read returned another ticket")
		}
		encoded, err := trackercontract.EncodeTicket(value.Ticket)
		if err != nil {
			return WorkSourceView{}, err
		}
		id, key, digest, err := trackercontract.ObservationIdentity(value.Ticket)
		fresh, ok := value.Ticket.Freshness.(tracker.Fresh)
		if err != nil || !ok || fresh.Revision != value.Ticket.Revision || fresh.ObservedAt.IsZero() || fresh.ObservedAt.After(check.CheckedAt.Add(time.Minute)) {
			return WorkSourceView{}, fail(ports.FailureProtocolDrift, "source freshness does not match its revision")
		}
		check.Outcome = statestore.SourceObserved{Observation: statestore.BacklogObservation{ID: id, TicketKey: key, NativeRevision: value.Ticket.Revision, ContentDigest: digest, Ref: value.Ticket.Ref, Ticket: encoded, ObservedAt: fresh.ObservedAt, EvidenceRef: value.Ticket.EvidenceRef}}
		check.CheckedAt = fresh.ObservedAt
	case tracker.Unchanged:
		if previousErr != nil || value.Ref != lineage.Ref || value.Fresh.Revision != previous.Observation.NativeRevision || value.Fresh.ObservedAt.IsZero() || value.Fresh.ObservedAt.After(check.CheckedAt.Add(time.Minute)) {
			return WorkSourceView{}, fail(ports.FailureProtocolDrift, "unchanged source response does not prove the retained revision")
		}
		check.Outcome = statestore.SourceUnchanged{ObservationID: previous.ObservationID}
		check.CheckedAt = value.Fresh.ObservedAt
	case tracker.Missing:
		if value.Ref != lineage.Ref || value.EvidenceRef == "" || value.ObservedAt.IsZero() || value.ObservedAt.After(check.CheckedAt.Add(time.Minute)) {
			return WorkSourceView{}, fail(ports.FailureProtocolDrift, "missing source response lacks exact evidence")
		}
		check.Outcome = statestore.SourceMissing{EvidenceRef: value.EvidenceRef}
		check.CheckedAt = value.ObservedAt
	default:
		return WorkSourceView{}, fail(ports.FailureProtocolDrift, "unknown exact source-read outcome")
	}
	if err := s.store.RecordSourceCheck(ctx, check); err != nil {
		return WorkSourceView{}, err
	}
	return s.Inspect(ctx, work)
}

func (s *Service) recordAccessLoss(ctx context.Context, lineage statestore.SourceLineage, pin tracker.Pin, err error) error {
	var problem *ports.Failure
	if errors.As(err, &problem) && (problem.Code == ports.FailurePermissionDenied || problem.Code == ports.FailureUnauthenticated) {
		if pin.CapabilitiesDigest == "" {
			if approved, readErr := s.store.LatestTicketAdmission(ctx, lineage.WorkID); readErr == nil {
				if snapshot, snapshotErr := s.store.ApprovedRunSource(ctx, lineage.WorkID, approved.ObservationID); snapshotErr == nil {
					pin = snapshot.Pin
				}
			}
		}
		if saveErr := s.store.RecordSourceCheck(ctx, statestore.SourceCheckMutation{WorkID: lineage.WorkID, ExpectedLineageRevision: lineage.Revision, Pin: pin, CheckedAt: s.options.Now().UTC(), Outcome: statestore.SourceInaccessible{Reason: "source access unavailable"}}); saveErr != nil && !missing(saveErr) {
			return saveErr
		}
		return fail(problem.Code, "pinned source access is unavailable; active inputs and history are retained")
	}
	return fail(ports.FailureUnavailable, "pinned source could not be refreshed")
}

func cancelled(ticket tracker.Ticket) bool {
	state, known := ticket.BusinessState.(tracker.Known[tracker.NamedID])
	if ticket.Ref.Namespace.Provider == "built_in" && known && state.Value.ID == "cancelled" {
		return true
	}
	return false
}

func validKey(value string) bool {
	return len(value) >= 8 && len(value) <= 128 && strings.TrimSpace(value) == value
}

func missing(err error) bool {
	var failure *ports.Failure
	return errors.Is(err, statestore.ErrNotFound) || errors.As(err, &failure) && failure.Code == ports.FailureNotFound
}

func fail(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}

func digest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
