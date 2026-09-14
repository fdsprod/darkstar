package runexecution_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"darkstar/src/adapters/provider/fake"
	. "darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports"
	"darkstar/src/ports/provider"
)

// The real execution service invokes this coordination boundary. The CLI's
// repository lock tests separately prove its SQLite lease/fencing behavior.
func TestWorkflowWriterCoordinationCoversProviderLifetime(t *testing.T) {
	for _, test := range []struct {
		name    string
		result  provider.AttemptResult
		settled bool
	}{
		{name: "success", result: provider.SucceededResult{StructuredOutput: json.RawMessage(`{"artifact":"candidate"}`)}, settled: true},
		{name: "failure", result: provider.FailedResult{Failure: ports.Failure{Code: ports.FailureUnavailable, Message: "provider failed"}}, settled: true},
		{name: "cancelled", result: provider.CancelledResult{}, settled: true},
		{name: "unknown", result: provider.UnknownResult{Failure: ports.Failure{Code: ports.FailureUncertain, Message: "provider termination cannot be proven"}}, settled: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, database, _ := newControlTestService(t, false)
			_, workID := seedWorkflowWork(t, database)
			planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
			if err := service.SetWorkflowPlanner(planner); err != nil {
				t.Fatal(err)
			}
			guard := &writerGuardBuilder{released: make(chan bool, 1)}
			factory := &guardedWriterFactory{guard: guard, result: test.result, readingResult: make(chan struct{}), allowResult: make(chan struct{})}
			if err := service.SetWorkflowDispatch(factory, guard); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Create(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "writer-lifetime-"+test.name); err != nil {
				t.Fatal(err)
			}
			select {
			case <-factory.readingResult:
			case <-time.After(5 * time.Second):
				t.Fatal("provider did not reach result collection under the writer guard")
			}
			if !guard.held.Load() {
				t.Fatal("repository guard was released before provider result collection")
			}
			select {
			case <-guard.released:
				t.Fatal("writer released while the provider was still active")
			default:
			}
			close(factory.allowResult)
			select {
			case settled := <-guard.released:
				if settled != test.settled {
					t.Fatalf("writer settled = %v, want %v", settled, test.settled)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("execution did not settle repository coordination")
			}
			if guard.held.Load() != !test.settled {
				t.Fatal("uncertain provider outcome must retain writer ownership")
			}
		})
	}
}

type writerGuardContextKey struct{}

type writerGuardBuilder struct {
	capturingAttemptBuilder
	held     atomic.Bool
	released chan bool
}

func (builder *writerGuardBuilder) AcquireWorkflowWrite(ctx context.Context, _ AttemptRequestContext) (context.Context, func(bool) error, error) {
	if !builder.held.CompareAndSwap(false, true) {
		return ctx, nil, errors.New("repository already has a writer")
	}
	finish := func(settled bool) error {
		if settled {
			builder.held.Store(false)
		}
		builder.released <- settled
		return nil
	}
	return context.WithValue(ctx, writerGuardContextKey{}, builder), finish, nil
}

type guardedWriterFactory struct {
	guard         *writerGuardBuilder
	result        provider.AttemptResult
	readingResult chan struct{}
	allowResult   chan struct{}
}

func (factory *guardedWriterFactory) Provider(_ context.Context, request ProviderRequest) (provider.Provider, error) {
	adapter, err := fake.New(fake.Scenario{
		Health:   provider.Health{State: provider.HealthAvailable, Provider: ProviderCodex, ProviderVersion: "test"},
		Attempts: []fake.AttemptScenario{{AttemptID: request.AttemptID, Result: factory.result}},
	})
	if err != nil {
		return nil, err
	}
	return &guardedWriterProvider{Provider: adapter, factory: factory}, nil
}

type guardedWriterProvider struct {
	provider.Provider
	factory *guardedWriterFactory
}

func (adapter *guardedWriterProvider) StartAttempt(ctx context.Context, request provider.AttemptRequest) (provider.AttemptHandle, error) {
	if !adapter.factory.guard.held.Load() || ctx.Value(writerGuardContextKey{}) != adapter.factory.guard {
		return provider.AttemptHandle{}, errors.New("provider started without repository coordination")
	}
	return adapter.Provider.StartAttempt(ctx, request)
}

func (adapter *guardedWriterProvider) GetResult(ctx context.Context, request provider.ResultRequest) (provider.AttemptResult, error) {
	if !adapter.factory.guard.held.Load() || ctx.Value(writerGuardContextKey{}) != adapter.factory.guard {
		return nil, errors.New("provider outlived repository coordination")
	}
	close(adapter.factory.readingResult)
	select {
	case <-adapter.factory.allowResult:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return adapter.Provider.GetResult(ctx, request)
}
