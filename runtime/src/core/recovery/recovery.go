// Package recovery classifies durable work before normal scheduling begins.
package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SubjectKind identifies the durable record being reconciled.
type SubjectKind string

const (
	SubjectLease     SubjectKind = "lease"
	SubjectOperation SubjectKind = "operation"
)

// Outcome is the closed set of startup reconciliation decisions.
type Outcome string

const (
	OutcomeAdopt             Outcome = "adopt"
	OutcomeResume            Outcome = "resume"
	OutcomeRetry             Outcome = "retry"
	OutcomeInterrupt         Outcome = "interrupt"
	OutcomeReconcileRequired Outcome = "reconcile_required"
)

// Subject is one non-terminal durable record. Authority selects the observer
// that owns the external proof; Payload is an immutable observation request.
type Subject struct {
	Kind      SubjectKind
	ID        string
	Authority string
	State     string
	Payload   json.RawMessage
}

// Decision records one authority-backed classification. Evidence must be a
// JSON object suitable for durable audit storage.
type Decision struct {
	Outcome  Outcome
	Evidence json.RawMessage
}

// Result pairs a subject with the decision committed during this pass.
type Result struct {
	Subject  Subject
	Decision Decision
}

// Report describes the complete startup pass. Scheduler admission and counts
// are derived from its committed results rather than stored as parallel flags.
type Report struct {
	Results []Result
}

// ReconcileRequired returns the number of subjects fenced for operator review.
func (report Report) ReconcileRequired() int {
	count := 0
	for _, result := range report.Results {
		if result.Decision.Outcome == OutcomeReconcileRequired {
			count++
		}
	}
	return count
}

// SchedulingAllowed derives scheduler admission from the committed results.
func (report Report) SchedulingAllowed() bool { return report.ReconcileRequired() == 0 }

// Store owns authoritative recovery records and their atomic transitions.
type Store interface {
	CheckIntegrity(context.Context) error
	RebuildProjections(context.Context) error
	PendingRecovery(context.Context) ([]Subject, error)
	ApplyRecovery(context.Context, string, Subject, Decision) error
}

// Observer reads the external authority for one subject without mutating it.
type Observer interface {
	Observe(context.Context, Subject) (Decision, error)
}

// ObserverFunc adapts a function into an Observer.
type ObserverFunc func(context.Context, Subject) (Decision, error)

func (f ObserverFunc) Observe(ctx context.Context, subject Subject) (Decision, error) {
	return f(ctx, subject)
}

// Reconciler completes recovery before a scheduler may admit new work.
type Reconciler struct {
	store     Store
	observers map[string]Observer
}

// New constructs a reconciler. Missing authority observers fail closed by
// committing reconcile_required rather than replaying a possible effect.
func New(store Store, observers map[string]Observer) (*Reconciler, error) {
	if store == nil {
		return nil, errors.New("recovery store is required")
	}
	owned := make(map[string]Observer, len(observers))
	for authority, observer := range observers {
		if strings.TrimSpace(authority) == "" || observer == nil {
			return nil, errors.New("recovery observers require a non-empty authority and implementation")
		}
		owned[authority] = observer
	}
	return &Reconciler{store: store, observers: owned}, nil
}

// Run rebuilds derived state, observes every pending subject in deterministic
// order, and durably commits each decision before returning.
func (r *Reconciler) Run(ctx context.Context, startupID string) (Report, error) {
	if strings.TrimSpace(startupID) == "" || len(startupID) > 128 {
		return Report{}, errors.New("startup ID must be between 1 and 128 bytes")
	}
	if err := r.store.CheckIntegrity(ctx); err != nil {
		return Report{}, fmt.Errorf("check durable state integrity: %w", err)
	}
	if err := r.store.RebuildProjections(ctx); err != nil {
		return Report{}, fmt.Errorf("rebuild projections: %w", err)
	}
	subjects, err := r.store.PendingRecovery(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load pending recovery: %w", err)
	}
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].Kind != subjects[j].Kind {
			return subjectOrder(subjects[i].Kind) < subjectOrder(subjects[j].Kind)
		}
		return subjects[i].ID < subjects[j].ID
	})

	report := Report{Results: make([]Result, 0, len(subjects))}
	seen := make(map[string]struct{}, len(subjects))
	for _, pending := range subjects {
		subject, err := normalizeSubject(pending)
		if err != nil {
			return Report{}, err
		}
		key := string(subject.Kind) + "\x00" + subject.ID
		if _, exists := seen[key]; exists {
			return Report{}, fmt.Errorf("duplicate recovery subject %s %s", subject.Kind, subject.ID)
		}
		seen[key] = struct{}{}

		decision := missingObserverDecision(subject.Authority)
		if observer, ok := r.observers[subject.Authority]; ok {
			decision, err = observer.Observe(ctx, subject)
			if err != nil {
				if ctx.Err() != nil {
					return Report{}, ctx.Err()
				}
				decision = observationFailureDecision(subject.Authority)
			}
		}
		decision, err = normalizeDecision(decision)
		if err != nil {
			return Report{}, fmt.Errorf("classify %s %s: %w", subject.Kind, subject.ID, err)
		}
		if err := validateDecisionForSubject(subject, decision); err != nil {
			return Report{}, fmt.Errorf("classify %s %s: %w", subject.Kind, subject.ID, err)
		}
		if err := r.store.ApplyRecovery(ctx, startupID, subject, decision); err != nil {
			return Report{}, fmt.Errorf("apply %s to %s %s: %w", decision.Outcome, subject.Kind, subject.ID, err)
		}
		report.Results = append(report.Results, Result{Subject: cloneSubject(subject), Decision: decision})
	}
	return report, nil
}

func subjectOrder(kind SubjectKind) int {
	switch kind {
	case SubjectLease:
		return 0
	case SubjectOperation:
		return 1
	default:
		return 2
	}
}

func validateSubject(subject Subject) error {
	_, err := normalizeSubject(subject)
	return err
}

func normalizeSubject(subject Subject) (Subject, error) {
	if subject.Kind != SubjectLease && subject.Kind != SubjectOperation {
		return Subject{}, fmt.Errorf("invalid recovery subject kind %q", subject.Kind)
	}
	if strings.TrimSpace(subject.ID) == "" || strings.TrimSpace(subject.Authority) == "" || strings.TrimSpace(subject.State) == "" {
		return Subject{}, fmt.Errorf("recovery subject %s requires ID, authority, and state", subject.Kind)
	}
	if (subject.Kind == SubjectLease && subject.State != "held" && subject.State != "releasing") ||
		(subject.Kind == SubjectOperation && subject.State != "prepared" && subject.State != "leased") {
		return Subject{}, fmt.Errorf("invalid %s recovery state %q", subject.Kind, subject.State)
	}
	payload, err := normalizeJSONObject(subject.Payload)
	if err != nil {
		return Subject{}, fmt.Errorf("recovery subject %s %s payload: %w", subject.Kind, subject.ID, err)
	}
	subject.Payload = payload
	return subject, nil
}

func normalizeDecision(decision Decision) (Decision, error) {
	switch decision.Outcome {
	case OutcomeAdopt, OutcomeResume, OutcomeRetry, OutcomeInterrupt, OutcomeReconcileRequired:
	default:
		return Decision{}, fmt.Errorf("invalid recovery outcome %q", decision.Outcome)
	}
	evidence, err := normalizeJSONObject(decision.Evidence)
	if err != nil {
		return Decision{}, fmt.Errorf("decision evidence: %w", err)
	}
	decision.Evidence = evidence
	return decision, nil
}

func validateDecisionForSubject(subject Subject, decision Decision) error {
	switch subject.Kind {
	case SubjectLease:
		if decision.Outcome == OutcomeResume || decision.Outcome == OutcomeRetry ||
			decision.Outcome == OutcomeInterrupt || decision.Outcome == OutcomeReconcileRequired {
			return nil
		}
	case SubjectOperation:
		if decision.Outcome == OutcomeAdopt || decision.Outcome == OutcomeResume ||
			decision.Outcome == OutcomeRetry || decision.Outcome == OutcomeReconcileRequired {
			return nil
		}
	}
	return fmt.Errorf("outcome %s is invalid for %s", decision.Outcome, subject.Kind)
}

func normalizeJSONObject(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, errors.New("must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errors.New("must be valid JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain one JSON value")
	}
	if _, ok := decoded.(map[string]any); !ok {
		return nil, errors.New("must be a JSON object")
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("normalize JSON object: %w", err)
	}
	return normalized, nil
}

func missingObserverDecision(authority string) Decision {
	evidence, _ := json.Marshal(map[string]any{"reason": "authority_observer_unavailable", "authority": authority})
	return Decision{Outcome: OutcomeReconcileRequired, Evidence: evidence}
}

func observationFailureDecision(authority string) Decision {
	evidence, _ := json.Marshal(map[string]any{"reason": "authority_observation_failed", "authority": authority})
	return Decision{Outcome: OutcomeReconcileRequired, Evidence: evidence}
}

func cloneSubject(subject Subject) Subject {
	subject.Payload = append(json.RawMessage(nil), subject.Payload...)
	return subject
}

// NormalizeDecision validates and canonicalizes a decision for adapters that
// apply recovery without going through Reconciler.Run.
func NormalizeDecision(decision Decision) (Decision, error) { return normalizeDecision(decision) }

// ValidateSubject checks the durable subject boundary for adapters.
func ValidateSubject(subject Subject) error { return validateSubject(subject) }

// NormalizeSubject validates and canonicalizes a subject for durable compare.
func NormalizeSubject(subject Subject) (Subject, error) { return normalizeSubject(subject) }

// ValidateDecisionForSubject rejects outcomes that cannot be represented by
// the subject's durable lifecycle.
func ValidateDecisionForSubject(subject Subject, decision Decision) error {
	return validateDecisionForSubject(subject, decision)
}
