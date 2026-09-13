package trackercontract_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

var now = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

type sourceFake struct {
	manifest      tracker.Manifest
	ticket        tracker.Ticket
	continuations map[string]tracker.Query
}

var _ worksource.TrackerSourceV1 = (*sourceFake)(nil)
var _ worksource.TrackerBrowserV1 = (*sourceFake)(nil)

func (source *sourceFake) Discover(context.Context, tracker.AdapterConfigPin) (tracker.Manifest, error) {
	return source.manifest, nil
}

func (source *sourceFake) Read(_ context.Context, request worksource.ReadTicketRequest) (tracker.ReadResult, error) {
	if err := trackercontract.ValidatePin(request.Pin, source.manifest.Pin); err != nil {
		return nil, err
	}
	if request.Ref != source.ticket.Ref {
		return tracker.Missing{Ref: request.Ref, ObservedAt: now, EvidenceRef: "missing-observation"}, nil
	}
	if request.KnownRevision == source.ticket.Revision {
		return tracker.Unchanged{Ref: request.Ref, Fresh: tracker.Fresh{ObservedAt: now, Revision: source.ticket.Revision}}, nil
	}
	return tracker.Found{Ticket: source.ticket}, nil
}

func (source *sourceFake) Browse(_ context.Context, request worksource.BrowseTicketsRequest) (tracker.TicketPage, error) {
	if err := trackercontract.ValidateQuery(request.Pin, source.manifest, request.Query); err != nil {
		return tracker.TicketPage{}, err
	}
	if request.Scope != source.manifest.Scope {
		return tracker.TicketPage{}, &ports.Failure{Code: ports.FailureConflict}
	}
	if request.Query.Cursor != "" {
		previous, exists := source.continuations[request.Query.Cursor]
		query := request.Query
		query.Cursor = ""
		if !exists || !reflect.DeepEqual(previous, query) {
			return tracker.TicketPage{}, &ports.Failure{Code: ports.FailureConflict, Message: "cursor query changed or cursor is unknown"}
		}
		return tracker.TicketPage{Next: tracker.End{}, Freshness: tracker.Fresh{ObservedAt: now, Revision: "page-2"}}, nil
	}
	source.continuations["opaque-next"] = request.Query
	return tracker.TicketPage{Tickets: []tracker.Ticket{source.ticket}, Next: tracker.More{Cursor: "opaque-next"}, Freshness: tracker.Fresh{ObservedAt: now, Revision: "page-1"}}, nil
}

// This fixture is a reference implementation of the port protocol only.
// It is deliberately not a provider API emulator or production built-in adapter.
type writerFake struct {
	source    *sourceFake
	options   tracker.WriteOptions
	receipts  map[string]tracker.Receipt
	mutations int
}

var _ ticketwriter.WriterV1 = (*writerFake)(nil)

func (writer *writerFake) Discover(context.Context, tracker.AdapterConfigPin) (tracker.Manifest, error) {
	return writer.source.manifest, nil
}

func (writer *writerFake) Inspect(context.Context, ticketwriter.InspectRequest) (tracker.WriteOptions, error) {
	return writer.options, nil
}

func (writer *writerFake) Apply(_ context.Context, intent tracker.Intent) (tracker.EffectResult, error) {
	if receipt, exists := writer.receipts[intent.OperationID]; exists {
		if err := trackercontract.ValidateReceipt(intent, receipt); err != nil {
			return nil, err
		}
		return tracker.Applied{Receipt: receipt}, nil
	}
	if err := trackercontract.ValidateIntent(intent, writer.source.manifest, writer.options, now); err != nil {
		return nil, err
	}
	writer.mutations++
	writer.receipts[intent.OperationID] = tracker.Receipt{
		OperationID: intent.OperationID, DesiredDigest: intent.DesiredDigest,
		Pin: intent.Pin, Destination: intent.Destination, Target: writer.source.ticket.Ref,
		ObservedRevision: "read-back-2", ObservedAt: now, EvidenceRef: "retained-read-back",
	}
	// Simulate mutation reaching the provider followed by a lost response.
	return tracker.Uncertain{Reason: "response lost", RecoveryRef: intent.OperationID}, nil
}

func (writer *writerFake) Reconcile(_ context.Context, intent tracker.Intent) (tracker.EffectResult, error) {
	if receipt, exists := writer.receipts[intent.OperationID]; exists {
		if err := trackercontract.ValidateReceipt(intent, receipt); err != nil {
			return nil, err
		}
		return tracker.Applied{Receipt: receipt}, nil
	}
	return tracker.Uncertain{Reason: "absence not proven by search", RecoveryRef: intent.OperationID}, nil
}

func fixture(provider string) (*sourceFake, *writerFake, tracker.Intent) {
	pin := tracker.Pin{
		AdapterConfigPin: tracker.AdapterConfigPin{
			ContractVersion: tracker.Version, AdapterID: provider + ".fixture", AdapterVersion: "1.0.0",
			InstallationID: "installation-1", AccountID: "account-1", BindingRevision: "binding-7",
			ConfigRevision: "config-3", ConfigDigest: "config-sha",
		},
		CapabilitiesDigest: "manifest-sha",
	}
	namespace := tracker.Namespace{Provider: provider, Host: "fixture.local", TenantID: "tenant-1", ScopeID: "stable-native-namespace"}
	scope := tracker.Scope{Namespace: namespace, ContainerID: "team-or-repository-1"}
	manifest := tracker.Manifest{Pin: pin, Scope: scope, MaxPageSize: 100, ObservedAt: now, EvidenceRef: "capability-read",
		Capabilities: make(map[tracker.Capability]tracker.Knowledge[bool]),
		Filters:      map[string][]tracker.FilterOperator{"status-id": {tracker.Equals, tracker.In}},
	}
	for _, capability := range []tracker.Capability{tracker.Fetch, tracker.List, tracker.Search, tracker.Filter, tracker.Page, tracker.Refresh, tracker.Create, tracker.Edit, tracker.Progress, tracker.Transitions, tracker.Hierarchy, tracker.Dependencies, tracker.ManagedLinks, tracker.Reconciliation} {
		manifest.Capabilities[capability] = tracker.Known[bool]{Value: true}
	}
	ticket := tracker.Ticket{Ref: tracker.TicketRef{Namespace: namespace, ID: "immutable-ticket-1"}, Revision: "native-1", Key: "VISIBLE-1", Title: "Business ticket",
		BusinessState: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "status-17", Name: "Awaiting release review"}},
		IssueType:     tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "type-9", Name: "Change"}},
		Sprint:        tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{{ID: "sprint-5", Name: "September"}}},
		Freshness:     tracker.Fresh{ObservedAt: now, Revision: "native-1"}, EvidenceRef: "ticket-read",
	}
	target := tracker.TicketScope{Ref: ticket.Ref, Revision: ticket.Revision, WorkflowRevision: "workflow-11", IssueTypeID: "type-9", StateID: "status-17", Sprint: ticket.Sprint}
	transition := tracker.Transition{Identity: tracker.NamedID{ID: "transition-42", Name: "Authorize deployment"}, ToState: tracker.NamedID{ID: "status-88", Name: "Release accepted"},
		Fields: []tracker.Field{{Identity: tracker.NamedID{ID: "resolution-id", Name: "Resolution"}, Required: true, Constraint: tracker.IDsConstraint{Choices: tracker.AllowedIDs{Values: []string{"resolution-3"}}}}},
		Guards: []tracker.Guard{{Identity: tracker.NamedID{ID: "guard-sprint", Name: "Active sprint"}, Satisfied: tracker.Known[bool]{Value: true}, EvidenceRef: "sprint-condition-read"}},
	}
	transitionFields := map[string]tracker.FieldValue{"resolution-id": tracker.IDsValue{"resolution-3"}}
	switch provider {
	case "github_issues":
		ticket.BusinessState = tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "open", Name: "Open"}}
		ticket.BusinessStateReason = tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "reopened", Name: "Reopened"}}
		ticket.Sprint = tracker.Unsupported[[]tracker.NamedID]{Reason: "GitHub Projects and sprint fields are excluded"}
		target.StateID = "open"
		target.Sprint = ticket.Sprint
		target.WorkflowRevision = "github-issues-api-v1"
		transition = tracker.Transition{Identity: tracker.NamedID{ID: "close:completed", Name: "Close as completed"}, ToState: tracker.NamedID{ID: "closed", Name: "Closed"}}
		transitionFields = nil
	case "linear":
		ticket.BusinessState = tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "team-state-uuid-1", Name: "Ready for verification"}}
		ticket.Sprint = tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{{ID: "cycle-uuid-1", Name: "Cycle 12"}}}
		target.StateID = "team-state-uuid-1"
		target.Sprint = ticket.Sprint
		transition = tracker.Transition{Identity: tracker.NamedID{ID: "set-state:team-state-uuid-2", Name: "Move to accepted"}, ToState: tracker.NamedID{ID: "team-state-uuid-2", Name: "Accepted"}}
		transitionFields = nil
	}
	options := tracker.WriteOptions{Pin: pin, Scope: target, Fields: tracker.Known[[]tracker.Field]{Value: []tracker.Field{{Identity: tracker.NamedID{ID: "title"}, Constraint: tracker.TextConstraint{}}}},
		Transitions: tracker.Known[[]tracker.Transition]{Value: []tracker.Transition{transition}}, ObservedAt: now, ValidUntil: now.Add(time.Minute), EvidenceRef: "ticket-write-options",
	}
	source := &sourceFake{manifest: manifest, ticket: ticket, continuations: make(map[string]tracker.Query)}
	writer := &writerFake{source: source, options: options, receipts: make(map[string]tracker.Receipt)}
	intent := tracker.Intent{OperationID: "operation-1", DesiredDigest: strings.Repeat("a", 64), AuthorizationRef: "verified-rule-decision-1", Pin: pin, Destination: scope,
		Effect: tracker.TakeTransition{Target: target, TransitionID: transition.Identity.ID, Fields: transitionFields},
	}
	return source, writer, intent
}

func requireCode(t *testing.T, err error, code ports.FailureCode) {
	t.Helper()
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestReferenceSourcesAndLostResponseReconciliation(t *testing.T) {
	for _, provider := range []string{"built_in", "linear", "github_issues", "jira_fixture"} {
		t.Run(provider, func(t *testing.T) {
			source, writer, intent := fixture(provider)
			ctx := context.Background()
			var readOnly worksource.TrackerSourceV1 = source
			if _, ok := readOnly.(ticketwriter.PublisherV1); ok {
				t.Fatal("source unexpectedly exposes writer authority")
			}
			before := source.ticket
			query := tracker.Query{Text: "Business", PageSize: 1, Predicates: []tracker.Predicate{{FieldID: "status-id", Operator: tracker.Equals, Values: []string{writer.options.Scope.(tracker.TicketScope).StateID}}}}
			page, err := source.Browse(ctx, worksource.BrowseTicketsRequest{Pin: intent.Pin, Scope: intent.Destination, Query: query})
			if err != nil || len(page.Tickets) != 1 {
				t.Fatalf("browse: %v, %v", page, err)
			}
			next := page.Next.(tracker.More)
			query.Cursor = next.Cursor
			last, err := source.Browse(ctx, worksource.BrowseTicketsRequest{Pin: intent.Pin, Scope: intent.Destination, Query: query})
			if err != nil {
				t.Fatal(err)
			}
			if _, ended := last.Next.(tracker.End); !ended {
				t.Fatal("pagination did not terminate")
			}
			query.Text = "changed-query"
			_, err = source.Browse(ctx, worksource.BrowseTicketsRequest{Pin: intent.Pin, Scope: intent.Destination, Query: query})
			requireCode(t, err, ports.FailureConflict)
			read, err := source.Read(ctx, worksource.ReadTicketRequest{Pin: intent.Pin, Ref: source.ticket.Ref, KnownRevision: source.ticket.Revision})
			if err != nil {
				t.Fatal(err)
			}
			if _, unchanged := read.(tracker.Unchanged); !unchanged || !reflect.DeepEqual(before, source.ticket) || writer.mutations != 0 {
				t.Fatal("observation altered business state or caused a write")
			}
			result, err := writer.Apply(ctx, intent)
			if err != nil || trackercontract.RetryAfterReconciliation(intent, result) {
				t.Fatalf("lost response must reconcile: %v", err)
			}
			result, err = writer.Reconcile(ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			applied, ok := result.(tracker.Applied)
			if !ok || trackercontract.ValidateReceipt(intent, applied.Receipt) != nil || writer.mutations != 1 {
				t.Fatal("reconciliation did not recover exactly one mutation")
			}
			intent.DesiredDigest = "different-intent"
			_, err = writer.Apply(ctx, intent)
			requireCode(t, err, ports.FailureConflict)
			if writer.mutations != 1 {
				t.Fatal("operation reuse duplicated mutation")
			}
		})
	}
}

func TestPreflightRejectsDriftAndUnknownCapabilities(t *testing.T) {
	tests := []struct {
		name   string
		change func(*sourceFake, *writerFake, *tracker.Intent)
		code   ports.FailureCode
	}{
		{"configuration", func(s *sourceFake, _ *writerFake, _ *tracker.Intent) {
			s.manifest.Pin.ConfigDigest = "changed"
		}, ports.FailureProtocolDrift},
		{"capability missing", func(s *sourceFake, _ *writerFake, _ *tracker.Intent) {
			delete(s.manifest.Capabilities, tracker.Transitions)
		}, ports.FailureProtocolDrift},
		{"unsupported", func(s *sourceFake, _ *writerFake, _ *tracker.Intent) {
			s.manifest.Capabilities[tracker.Transitions] = tracker.Unsupported[bool]{Reason: "no permission"}
		}, ports.FailureUnsupported},
		{"status", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			target := w.options.Scope.(tracker.TicketScope)
			target.StateID = "status-other"
			w.options.Scope = target
		}, ports.FailureConflict},
		{"issue type", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			target := w.options.Scope.(tracker.TicketScope)
			target.IssueTypeID = "bug"
			w.options.Scope = target
		}, ports.FailureConflict},
		{"workflow", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			target := w.options.Scope.(tracker.TicketScope)
			target.WorkflowRevision = "new"
			w.options.Scope = target
		}, ports.FailureConflict},
		{"sprint independently changed", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			target := w.options.Scope.(tracker.TicketScope)
			target.Sprint = tracker.Known[[]tracker.NamedID]{}
			w.options.Scope = target
		}, ports.FailureConflict},
		{"stale", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			w.options.ValidUntil = now
		}, ports.FailureConflict},
		{"English name", func(_ *sourceFake, _ *writerFake, i *tracker.Intent) {
			effect := i.Effect.(tracker.TakeTransition)
			effect.TransitionID = "Authorize deployment"
			i.Effect = effect
		}, ports.FailureConflict},
		{"required field", func(_ *sourceFake, _ *writerFake, i *tracker.Intent) {
			effect := i.Effect.(tracker.TakeTransition)
			effect.Fields = nil
			i.Effect = effect
		}, ports.FailureInvalidRequest},
		{"wrong field", func(_ *sourceFake, _ *writerFake, i *tracker.Intent) {
			effect := i.Effect.(tracker.TakeTransition)
			effect.Fields["status"] = tracker.TextValue("Done")
			i.Effect = effect
		}, ports.FailureUnsupported},
		{"unknown guard", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			transitions := w.options.Transitions.(tracker.Known[[]tracker.Transition])
			transitions.Value[0].Guards[0].Satisfied = tracker.Unknown[bool]{Reason: "sprint not fetched"}
		}, ports.FailureConflict},
		{"false guard", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			transitions := w.options.Transitions.(tracker.Known[[]tracker.Transition])
			transitions.Value[0].Guards[0].Satisfied = tracker.Known[bool]{Value: false}
		}, ports.FailureConflict},
		{"duplicate transition", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			transitions := w.options.Transitions.(tracker.Known[[]tracker.Transition])
			transitions.Value = append(transitions.Value, transitions.Value[0])
			w.options.Transitions = transitions
		}, ports.FailureProtocolDrift},
		{"zero allowed choices", func(_ *sourceFake, w *writerFake, _ *tracker.Intent) {
			transitions := w.options.Transitions.(tracker.Known[[]tracker.Transition])
			transitions.Value[0].Fields[0].Constraint = tracker.IDsConstraint{Choices: tracker.AllowedIDs{}}
		}, ports.FailureInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, writer, intent := fixture("jira_fixture")
			test.change(source, writer, &intent)
			_, err := writer.Apply(context.Background(), intent)
			requireCode(t, err, test.code)
			if writer.mutations != 0 {
				t.Fatal("failed preflight performed an effect")
			}
		})
	}
}

func artifact() tracker.ArtifactRef {
	return tracker.ArtifactRef{ArtifactID: "artifact-1", Version: 1, SHA256: strings.Repeat("a", 64)}
}

func TestRelationshipsRequireNativeSupportOrVisibleConfiguredFallback(t *testing.T) {
	source, writer, intent := fixture("github_issues")
	effect := tracker.LinkStories{Target: writer.options.Scope.(tracker.TicketScope), Story: tracker.StoryRef{ProjectID: "project", FeatureKey: "feature", StoryKey: "story-1"}, RelatedStory: tracker.StoryRef{ProjectID: "project", FeatureKey: "feature", StoryKey: "story-2"}, Backlog: artifact(), Relation: tracker.Relation{Kind: tracker.DependsOn, Target: tracker.TicketRef{Namespace: intent.Destination.Namespace, ID: "ticket-2"}}, Representation: tracker.NativeRelation{}}
	intent.Effect = effect
	if err := trackercontract.ValidateIntent(intent, source.manifest, writer.options, now); err != nil {
		t.Fatal(err)
	}
	source.manifest.Capabilities[tracker.Dependencies] = tracker.Unsupported[bool]{Reason: "host does not provide dependency API"}
	requireCode(t, trackercontract.ValidateIntent(intent, source.manifest, writer.options, now), ports.FailureUnsupported)
	effect.Representation = tracker.ManagedRelation{FallbackRevision: "fallback-3", SectionID: "darkstar-relationships"}
	intent.Effect = effect
	if err := trackercontract.ValidateIntent(intent, source.manifest, writer.options, now); err != nil {
		t.Fatal(err)
	}
	effect.RelatedStory.FeatureKey = "another-feature"
	intent.Effect = effect
	requireCode(t, trackercontract.ValidateIntent(intent, source.manifest, writer.options, now), ports.FailureInvalidRequest)
	effect.RelatedStory.FeatureKey = "feature"
	effect.Representation = tracker.ManagedRelation{SectionID: "unconfigured"}
	intent.Effect = effect
	requireCode(t, trackercontract.ValidateIntent(intent, source.manifest, writer.options, now), ports.FailureInvalidRequest)
}

func TestDirectCreationProgressAndIndependentStateAxes(t *testing.T) {
	source, writer, intent := fixture("built_in")
	before := source.ticket.BusinessState
	writer.options.Scope = tracker.CreationScope{IssueTypeID: "type-9"}
	intent.Effect = tracker.CreateTicket{Origin: tracker.DirectCreation{Request: artifact()}, IssueTypeID: "type-9", Fields: map[string]tracker.FieldValue{"title": tracker.TextValue("Native ticket")}}
	if err := trackercontract.ValidateIntent(intent, source.manifest, writer.options, now); err != nil {
		t.Fatal(err)
	}
	// Native creation works, but the feature-publication workflow stays external.
	requireCode(t, trackercontract.ValidatePublicationDestination(intent.Destination), ports.FailureUnsupported)
	_, writer, intent = fixture("built_in")
	intent.Effect = tracker.ReportProgress{Target: writer.options.Scope.(tracker.TicketScope), Evidence: tracker.GeneralProgress{Artifacts: []tracker.ArtifactRef{artifact()}}, Body: "Waiting for clarification"}
	if err := trackercontract.ValidateIntent(intent, source.manifest, writer.options, now); err != nil {
		t.Fatal(err)
	}
	var sync tracker.SyncState = tracker.Pending{Intent: intent}
	executionOutcome := "completed"
	if executionOutcome != "completed" || !reflect.DeepEqual(before, source.ticket.BusinessState) || writer.mutations != 0 {
		t.Fatal("execution outcome affected ticket state")
	}
	if _, pending := sync.(tracker.Pending); !pending {
		t.Fatal("execution outcome advanced sync operation")
	}
}

func TestReceiptAndAbsenceProofAreBoundToIntent(t *testing.T) {
	_, writer, intent := fixture("linear")
	result, err := writer.Reconcile(context.Background(), intent)
	if err != nil || trackercontract.RetryAfterReconciliation(intent, result) {
		t.Fatal("empty search authorized retry")
	}
	absent := tracker.NotApplied{OperationID: intent.OperationID, DesiredDigest: intent.DesiredDigest, Pin: intent.Pin, Destination: intent.Destination, EvidenceRef: "authoritative-absence-proof"}
	if !trackercontract.RetryAfterReconciliation(intent, absent) {
		t.Fatal("matching absence proof did not permit retry consideration")
	}
	absent.OperationID = "another-operation"
	if trackercontract.RetryAfterReconciliation(intent, absent) {
		t.Fatal("unrelated absence proof authorized retry")
	}
	if _, err = writer.Apply(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	receipt := writer.receipts[intent.OperationID]
	receipt.Target.ID = "other-ticket"
	requireCode(t, trackercontract.ValidateReceipt(intent, receipt), ports.FailureConflict)
	// A team change changes binding scope, not the stable workspace ticket ID.
	identity := writer.source.ticket.Ref
	writer.source.manifest.Scope.ContainerID = "new-team"
	if writer.source.ticket.Ref != identity {
		t.Fatal("team move changed native identity")
	}
	requireCode(t, trackercontract.ValidateIntent(intent, writer.source.manifest, writer.options, now), ports.FailureConflict)
}

func TestQueryAndNumericFieldRejections(t *testing.T) {
	source, writer, intent := fixture("linear")
	requireCode(t, trackercontract.ValidateQuery(intent.Pin, source.manifest, tracker.Query{PageSize: 10, Predicates: []tracker.Predicate{{FieldID: "english-status-name", Operator: tracker.Equals, Values: []string{"Done"}}}}), ports.FailureUnsupported)
	writer.options.Fields = tracker.Known[[]tracker.Field]{Value: []tracker.Field{{Identity: tracker.NamedID{ID: "estimate"}, Constraint: tracker.NumberConstraint{}}}}
	for _, number := range []float64{math.NaN(), math.Inf(1)} {
		intent.Effect = tracker.EditTicket{Target: writer.options.Scope.(tracker.TicketScope), Fields: map[string]tracker.FieldValue{"estimate": tracker.NumberValue(number)}}
		requireCode(t, trackercontract.ValidateIntent(intent, source.manifest, writer.options, now), ports.FailureInvalidRequest)
	}
}

func TestProviderSpecificWorkflowObservations(t *testing.T) {
	github, writer, intent := fixture("github_issues")
	if _, unsupported := github.ticket.Sprint.(tracker.Unsupported[[]tracker.NamedID]); !unsupported {
		t.Fatal("GitHub Issues must not claim Projects/sprint support")
	}
	if github.ticket.BusinessState.(tracker.Known[tracker.NamedID]).Value.ID != "open" || github.ticket.BusinessStateReason.(tracker.Known[tracker.NamedID]).Value.ID != "reopened" {
		t.Fatal("GitHub state and reason were conflated")
	}
	transition := writer.options.Transitions.(tracker.Known[[]tracker.Transition]).Value[0]
	if len(transition.Fields) != 0 || len(transition.Guards) != 0 || transition.Identity.ID != "close:completed" {
		t.Fatal("GitHub fixture invented a custom workflow")
	}
	effect := intent.Effect.(tracker.TakeTransition)
	effect.TransitionID = "transition-42"
	intent.Effect = effect
	requireCode(t, trackercontract.ValidateIntent(intent, github.manifest, writer.options, now), ports.FailureConflict)
	effect.TransitionID = "close:completed"
	effect.Fields = map[string]tracker.FieldValue{"resolution-id": tracker.IDsValue{"resolution-3"}}
	intent.Effect = effect
	requireCode(t, trackercontract.ValidateIntent(intent, github.manifest, writer.options, now), ports.FailureUnsupported)
	linear, writer, _ := fixture("linear")
	if linear.ticket.BusinessState.(tracker.Known[tracker.NamedID]).Value.ID != "team-state-uuid-1" || linear.ticket.Sprint.(tracker.Known[[]tracker.NamedID]).Value[0].ID != "cycle-uuid-1" {
		t.Fatal("Linear state and cycle mapping lost stable IDs")
	}
	if len(writer.options.Transitions.(tracker.Known[[]tracker.Transition]).Value[0].Guards) != 0 {
		t.Fatal("Linear fixture invented Jira transition guards")
	}
	_, writer, _ = fixture("jira_fixture")
	transition = writer.options.Transitions.(tracker.Known[[]tracker.Transition]).Value[0]
	if writer.options.Scope.(tracker.TicketScope).IssueTypeID != "type-9" || len(transition.Fields) != 1 || len(transition.Guards) != 1 {
		t.Fatal("Jira fixture lacks type-specific requirements and independent sprint guard")
	}
}

func TestDiscoveryBootstrapAndUnknownDescriptors(t *testing.T) {
	source, writer, intent := fixture("built_in")
	discovered, err := source.Discover(context.Background(), intent.Pin.AdapterConfigPin)
	if err != nil || discovered.Pin.CapabilitiesDigest == "" || trackercontract.ValidatePin(intent.Pin, discovered.Pin) != nil {
		t.Fatal("bootstrap did not complete the exact capability pin")
	}
	writer.options.Fields = tracker.Known[[]tracker.Field]{Value: []tracker.Field{{Identity: tracker.NamedID{ID: "title"}}}}
	intent.Effect = tracker.EditTicket{Target: writer.options.Scope.(tracker.TicketScope), Fields: map[string]tracker.FieldValue{"title": tracker.TextValue("A title")}}
	requireCode(t, trackercontract.ValidateIntent(intent, source.manifest, writer.options, now), ports.FailureProtocolDrift)
}
