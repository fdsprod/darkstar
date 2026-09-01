package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRunRebuildsClassifiesInOrderAndBlocksOnAmbiguity(t *testing.T) {
	t.Parallel()
	store := &fakeStore{subjects: []Subject{
		{Kind: SubjectOperation, ID: "operation_b", Authority: "git", State: "prepared", Payload: json.RawMessage(`{}`)},
		{Kind: SubjectLease, ID: "lease_b", Authority: "process", State: "held", Payload: json.RawMessage(`{}`)},
		{Kind: SubjectLease, ID: "lease_a", Authority: "unknown", State: "held", Payload: json.RawMessage(`{}`)},
	}}
	reconciler, err := New(store, map[string]Observer{
		"process": ObserverFunc(func(context.Context, Subject) (Decision, error) {
			return Decision{Outcome: OutcomeResume, Evidence: json.RawMessage(`{"live":true}`)}, nil
		}),
		"git": ObserverFunc(func(context.Context, Subject) (Decision, error) {
			return Decision{Outcome: OutcomeRetry, Evidence: json.RawMessage(`{"absent":true}`)}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Run(context.Background(), "daemon_01")
	if err != nil {
		t.Fatal(err)
	}
	if !store.rebuilt {
		t.Fatal("Run did not rebuild projections")
	}
	if report.SchedulingAllowed() {
		t.Fatal("SchedulingAllowed() = true with an unavailable authority observer")
	}
	if report.ReconcileRequired() != 1 {
		t.Fatalf("ReconcileRequired() = %d, want 1", report.ReconcileRequired())
	}
	wantOrder := []string{"lease_a", "lease_b", "operation_b"}
	if !reflect.DeepEqual(store.appliedIDs, wantOrder) {
		t.Fatalf("apply order = %v, want %v", store.appliedIDs, wantOrder)
	}
	if got := report.Results[0].Decision.Outcome; got != OutcomeReconcileRequired {
		t.Fatalf("missing observer outcome = %s", got)
	}
}

func TestObserverFailureFailsClosedWithoutPersistingErrorText(t *testing.T) {
	t.Parallel()
	store := &fakeStore{subjects: []Subject{{Kind: SubjectOperation, ID: "operation_a", Authority: "remote", State: "prepared", Payload: json.RawMessage(`{}`)}}}
	reconciler, err := New(store, map[string]Observer{"remote": ObserverFunc(func(context.Context, Subject) (Decision, error) {
		return Decision{}, errors.New("secret remote diagnostic")
	})})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Run(context.Background(), "daemon_01")
	if err != nil {
		t.Fatal(err)
	}
	evidence := string(report.Results[0].Decision.Evidence)
	if evidence != `{"authority":"remote","reason":"authority_observation_failed"}` {
		t.Fatalf("evidence = %s", evidence)
	}
}

func TestRunRejectsInvalidObserverDecisionBeforeApplyingIt(t *testing.T) {
	t.Parallel()
	store := &fakeStore{subjects: []Subject{{Kind: SubjectLease, ID: "lease_a", Authority: "process", State: "held", Payload: json.RawMessage(`{}`)}}}
	reconciler, err := New(store, map[string]Observer{"process": ObserverFunc(func(context.Context, Subject) (Decision, error) {
		return Decision{Outcome: "guess", Evidence: json.RawMessage(`{}`)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Run(context.Background(), "daemon_01"); err == nil {
		t.Fatal("Run accepted an invalid recovery outcome")
	}
	if len(store.appliedIDs) != 0 {
		t.Fatalf("applied invalid decision to %v", store.appliedIDs)
	}
}

func TestRunRejectsOutcomeThatSubjectCannotRepresent(t *testing.T) {
	t.Parallel()
	store := &fakeStore{subjects: []Subject{{Kind: SubjectOperation, ID: "operation_a", Authority: "remote", State: "prepared", Payload: json.RawMessage(`{}`)}}}
	reconciler, err := New(store, map[string]Observer{"remote": ObserverFunc(func(context.Context, Subject) (Decision, error) {
		return Decision{Outcome: OutcomeInterrupt, Evidence: json.RawMessage(`{"terminal":true}`)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Run(context.Background(), "daemon_01"); err == nil {
		t.Fatal("Run accepted interrupt for an operation projection")
	}
}

func TestNormalizeSubjectPreservesLargeIntegerIdentity(t *testing.T) {
	t.Parallel()
	subject, err := NormalizeSubject(Subject{
		Kind: SubjectLease, ID: "lease_a", Authority: "process", State: "held",
		Payload: json.RawMessage(`{ "fencingToken": 18446744073709551615 }`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(subject.Payload) != `{"fencingToken":18446744073709551615}` {
		t.Fatalf("normalized payload = %s", subject.Payload)
	}
}

type fakeStore struct {
	subjects   []Subject
	rebuilt    bool
	appliedIDs []string
}

func (s *fakeStore) CheckIntegrity(context.Context) error { return nil }

func (s *fakeStore) RebuildProjections(context.Context) error {
	s.rebuilt = true
	return nil
}

func (s *fakeStore) PendingRecovery(context.Context) ([]Subject, error) {
	if !s.rebuilt {
		return nil, errors.New("pending recovery loaded before projection rebuild")
	}
	return append([]Subject(nil), s.subjects...), nil
}

func (s *fakeStore) ApplyRecovery(_ context.Context, _ string, subject Subject, _ Decision) error {
	s.appliedIDs = append(s.appliedIDs, subject.ID)
	return nil
}
