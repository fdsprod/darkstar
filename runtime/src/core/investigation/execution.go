package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func (s *Service) execute(ctx context.Context, claim statestore.InvestigationClaim) {
	id := claim.Attempt.AttemptID
	defer func() {
		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
		s.workers.Done()
		s.signal()
	}()
	heartbeatContext, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	go s.heartbeat(heartbeatContext, id)
	recorder := &attemptRecorder{store: s.store, id: id, owner: s.options.OwnerID}
	execution, err := s.execution(context.Background(), claim.Attempt)
	if err != nil {
		state := "failed"
		if claim.Reconcile {
			state = "uncertain"
		}
		_, _ = s.store.CompleteInvestigationAttempt(context.Background(), id, s.options.OwnerID, state, err.Error())
		return
	}
	var outcome Outcome
	if claim.Reconcile {
		outcome = s.worker.Reconcile(ctx, execution, claim.Attempt, recorder)
	} else if execution.CancelRequested {
		outcome = Outcome{State: "cancelled", Reason: "cancelled before provider dispatch"}
	} else {
		outcome = s.worker.Execute(ctx, execution, recorder)
	}
	if outcome.Result != nil && outcome.State == "succeeded" {
		if err = recorder.RecordResult(context.Background(), *outcome.Result); err != nil {
			outcome = Outcome{State: "uncertain", Reason: "validated result could not be retained: " + err.Error()}
		}
	}
	if outcome.State != "succeeded" && outcome.State != "failed" && outcome.State != "cancelled" && outcome.State != "uncertain" {
		outcome = Outcome{State: "uncertain", Reason: "worker returned an unknown terminal disposition"}
	}
	if outcome.State != "succeeded" && outcome.Reason == "" {
		outcome.Reason = "worker did not establish a successful result"
	}
	// A failed terminal write leaves the active claim recoverable. It is never
	// converted into an eligible new execution based on an in-memory outcome.
	_, _ = s.store.CompleteInvestigationAttempt(context.Background(), id, s.options.OwnerID, outcome.State, outcome.Reason)
}

func (s *Service) heartbeat(ctx context.Context, id string) {
	ticker := time.NewTicker(s.options.LeaseDuration / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.RenewInvestigationAttempt(ctx, id, s.options.OwnerID, s.options.LeaseDuration); err != nil {
				s.mu.Lock()
				cancel := s.active[id]
				s.mu.Unlock()
				if cancel != nil {
					cancel()
				}
				return
			}
		}
	}
}

func (s *Service) execution(ctx context.Context, attempt Attempt) (Execution, error) {
	view, err := s.Get(ctx, attempt.CollectionID)
	if err != nil {
		return Execution{}, err
	}
	result := Execution{CollectionID: attempt.CollectionID, ProjectID: view.Collection.ProjectID, UnitID: attempt.UnitID, AttemptID: attempt.AttemptID, Task: view.Collection.Task, Provider: view.Collection.Provider, Findings: []UnitResult{}, Missing: []MissingUnit{}, CancelRequested: view.Collection.Status == "cancelling"}
	ids := []string{}
	for _, unit := range view.Units {
		if unit.UnitID == attempt.UnitID {
			result.Kind = unit.Kind
			if unit.Kind == "repository" {
				ids = append(ids, unit.RepositoryID)
			}
		}
	}
	if result.Kind == "" {
		return Execution{}, errors.New("claimed investigation unit is missing")
	}
	result.Scope, err = s.scopes.BindAttempt(ctx, view.Collection.ScopeID, attempt.AttemptID, ids)
	if err != nil {
		return Execution{}, err
	}
	if result.Kind == "synthesis" {
		for _, unit := range view.Units {
			if unit.Kind != "repository" {
				continue
			}
			if unit.Status == "succeeded" && unit.Result != nil {
				result.Findings = append(result.Findings, *unit.Result)
			} else {
				reason := unit.Reason
				if reason == "" {
					reason = "repository evidence was not produced"
				}
				result.Missing = append(result.Missing, MissingUnit{RepositoryID: unit.RepositoryID, Reason: reason})
			}
		}
	}
	return result, nil
}

type attemptRecorder struct {
	store statestore.InvestigationStore
	id    string
	owner string
}

func (r *attemptRecorder) RecordPrepared(ctx context.Context, fingerprint, digest string, request json.RawMessage) error {
	return r.store.ObserveInvestigationAttempt(ctx, r.id, r.owner, statestore.InvestigationObservation{Kind: "prepared", CapabilityFingerprint: fingerprint, ContextDigest: digest, Request: request})
}

func (r *attemptRecorder) RecordHandle(ctx context.Context, handle provider.AttemptHandle, fingerprint, digest string) error {
	return r.store.ObserveInvestigationAttempt(ctx, r.id, r.owner, statestore.InvestigationObservation{Kind: "handle", Handle: &handle, CapabilityFingerprint: fingerprint, ContextDigest: digest})
}

func (r *attemptRecorder) RecordEvent(ctx context.Context, event provider.Event) error {
	return r.store.ObserveInvestigationAttempt(ctx, r.id, r.owner, statestore.InvestigationObservation{Kind: "event", Event: &event})
}

func (r *attemptRecorder) RecordSubmission(ctx context.Context, submission json.RawMessage) error {
	return r.store.ObserveInvestigationAttempt(ctx, r.id, r.owner, statestore.InvestigationObservation{Kind: "submission", Submission: submission})
}

func (r *attemptRecorder) RecordResult(ctx context.Context, result UnitResult) error {
	return r.store.ObserveInvestigationAttempt(ctx, r.id, r.owner, statestore.InvestigationObservation{Kind: "result", Result: &result})
}
