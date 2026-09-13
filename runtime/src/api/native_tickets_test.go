package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/core/ticketmanagement"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
)

func TestNativeTicketAPIUsesBusinessCapabilitiesWithoutChangingExecution(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "tickets.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	work, _ := workmanagement.New(database)
	project, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Native", Source: t.TempDir()}, "ticket-project-key")
	if err != nil {
		t.Fatal(err)
	}
	created, err := work.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Original", Details: "Original body", Priority: 3}, "ticket-work-key")
	if err != nil {
		t.Fatal(err)
	}
	service := nativeTicketTestService(t, database, database)
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetNativeTickets(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	base := "/api/v1/projects/" + project.ProjectID + "/tickets"
	read := workRequest(t, endpoint, http.MethodGet, base+"?q=Original&state=open&pageSize=1", "", "")
	var page ticketmanagement.Page
	decodeJSON(t, read, &page)
	_ = read.Body.Close()
	if page.SchemaVersion != 1 || len(page.Tickets) != 1 || page.Tickets[0].ID != created.WorkItemID || page.Tickets[0].BusinessState.ID != "open" {
		t.Fatalf("page = %#v", page)
	}
	resource := base + "/" + created.WorkItemID
	var detail ticketmanagement.Detail
	read = workRequest(t, endpoint, http.MethodGet, resource, "", "")
	decodeJSON(t, read, &detail)
	_ = read.Body.Close()
	if !detail.Capabilities.Edit || len(detail.Fields) == 0 || len(detail.History) != 1 || len(detail.Transitions) == 0 {
		t.Fatalf("detail = %#v", detail)
	}
	editBody := fmt.Sprintf(`{"schemaVersion":1,"revision":%q,"title":"Revised","description":"Revised body","priority":8}`, detail.Ticket.Revision)
	read = workRequest(t, endpoint, http.MethodPost, resource+"/edit", editBody, "native-edit-command")
	assertNativeStatus(t, read, http.StatusOK)
	decodeJSON(t, read, &detail)
	_ = read.Body.Close()
	if detail.Ticket.Title != "Revised" || detail.Ticket.Revision != "2" || len(detail.History) != 2 || detail.History[0].Title != "Original" {
		t.Fatalf("edited detail = %#v", detail)
	}
	// Exact replay returns the durable original response and adds no history.
	read = workRequest(t, endpoint, http.MethodPost, resource+"/edit", editBody, "native-edit-command")
	assertNativeStatus(t, read, http.StatusOK)
	decodeJSON(t, read, &detail)
	_ = read.Body.Close()
	if detail.Ticket.Revision != "2" || len(detail.History) != 2 {
		t.Fatalf("replayed detail = %#v", detail)
	}
	read = workRequest(t, endpoint, http.MethodPost, resource+"/edit", editBody, "native-stale-command")
	assertAPIError(t, read, http.StatusConflict, "TICKET_CONFLICT")
	_ = read.Body.Close()
	transitionBody := `{"schemaVersion":1,"revision":"2","transitionId":"set-state:completed"}`
	read = workRequest(t, endpoint, http.MethodPost, resource+"/transition", transitionBody, "native-state-command")
	assertNativeStatus(t, read, http.StatusOK)
	decodeJSON(t, read, &detail)
	_ = read.Body.Close()
	if detail.Ticket.BusinessState.ID != "completed" || len(detail.History) != 3 {
		t.Fatalf("transition detail = %#v", detail)
	}
	after, err := database.WorkItem(ctx, created.WorkItemID)
	if err != nil || after.Status != created.Status || after.Title != created.Title || after.ResourceVersion != created.ResourceVersion {
		t.Fatalf("business mutation changed execution: %#v; %v", after, err)
	}
	runs, err := database.RunsForWorkItem(ctx, created.WorkItemID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("browsing or editing scheduled execution: %v %v", runs, err)
	}
	read = workRequest(t, endpoint, http.MethodGet, base+"?state=open&state=completed", "", "")
	assertAPIError(t, read, http.StatusBadRequest, "TICKET_INVALID_REQUEST")
	_ = read.Body.Close()
}

type failingTicketCommandStore struct {
	statestore.Store
	fail bool
}

func (s *failingTicketCommandStore) CompleteCommand(ctx context.Context, request statestore.CompleteCommandRequest) (statestore.CommandEvidence, error) {
	if s.fail && strings.HasPrefix(request.Scope, "native-tickets/v1/") && !strings.HasSuffix(request.Scope, "/authorized-target") {
		s.fail = false
		return statestore.CommandEvidence{}, errors.New("simulated response persistence interruption")
	}
	return s.Store.CompleteCommand(ctx, request)
}

func TestNativeTicketCommandRecoversAfterAppliedResponseInterruption(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ticket-recovery.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	work, _ := workmanagement.New(database)
	project, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Native", Source: t.TempDir()}, "recovery-project")
	if err != nil {
		t.Fatal(err)
	}
	created, err := work.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Original"}, "recovery-work")
	if err != nil {
		t.Fatal(err)
	}
	store := &failingTicketCommandStore{Store: database, fail: true}
	service := nativeTicketTestService(t, store, database)
	title := "Retained edit"
	request := ticketmanagement.EditRequest{SchemaVersion: 1, Revision: "1", Title: &title}
	if _, err := service.Edit(ctx, project.ProjectID, created.WorkItemID, request, "recovery-edit"); err == nil {
		t.Fatal("expected response persistence interruption")
	}
	// A fresh service must load the saved authorization scope and reconcile the
	// already applied effect; current ticket state must not replace that scope.
	service = nativeTicketTestService(t, store, database)
	result, err := service.Edit(ctx, project.ProjectID, created.WorkItemID, request, "recovery-edit")
	if err != nil {
		t.Fatal(err)
	}
	if result.Ticket.Title != title || result.Ticket.Revision != "2" || len(result.History) != 2 {
		t.Fatalf("recovery duplicated mutation: %#v", result)
	}
}

func nativeTicketTestService(t *testing.T, store statestore.Store, native statestore.NativeTrackerStore) *ticketmanagement.Service {
	t.Helper()
	service, err := ticketmanagement.New(store, native, func(ctx context.Context, projectID string) (ticketmanagement.Binding, error) {
		adapter, err := builtin.New(native, projectID)
		if err != nil {
			return ticketmanagement.Binding{}, err
		}
		return ticketmanagement.Binding{Source: adapter, Browser: adapter, Writer: adapter, Config: adapter.ConfigPin()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertNativeStatus(t *testing.T, response *http.Response, expected int) {
	t.Helper()
	if response.StatusCode != expected {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d; want %d: %s", response.StatusCode, expected, body)
	}
}

type mismatchedTicketWriter struct {
	ticketwriter.WriterV1
	wrongAbsence bool
	applyCalled  bool
}

func (w *mismatchedTicketWriter) Reconcile(ctx context.Context, intent tracker.Intent) (tracker.EffectResult, error) {
	result, err := w.WriterV1.Reconcile(ctx, intent)
	if w.wrongAbsence {
		if absent, ok := result.(tracker.NotApplied); ok {
			absent.OperationID = "unrelated-operation"
			return absent, err
		}
	}
	return result, err
}

func (w *mismatchedTicketWriter) Apply(ctx context.Context, intent tracker.Intent) (tracker.EffectResult, error) {
	w.applyCalled = true
	result, err := w.WriterV1.Apply(ctx, intent)
	if applied, ok := result.(tracker.Applied); ok {
		applied.Receipt.Target.ID = "unrelated-ticket"
		return applied, err
	}
	return result, err
}

func TestNativeTicketDaemonRejectsUnrelatedAdapterProof(t *testing.T) {
	for _, wrongAbsence := range []bool{true, false} {
		t.Run(fmt.Sprintf("wrong-absence-%t", wrongAbsence), func(t *testing.T) {
			ctx := context.Background()
			database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "proof.db"), sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = database.Close()
			})
			work, _ := workmanagement.New(database)
			project, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Proof", Source: t.TempDir()}, "proof-project")
			if err != nil {
				t.Fatal(err)
			}
			created, err := work.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Original"}, "proof-work")
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := builtin.New(database, project.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			writer := &mismatchedTicketWriter{WriterV1: adapter, wrongAbsence: wrongAbsence}
			service, err := ticketmanagement.New(database, database, func(ctx context.Context, projectID string) (ticketmanagement.Binding, error) {
				return ticketmanagement.Binding{Source: adapter, Browser: adapter, Writer: writer, Config: adapter.ConfigPin()}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			title := "Changed"
			if _, err := service.Edit(ctx, project.ProjectID, created.WorkItemID, ticketmanagement.EditRequest{SchemaVersion: 1, Revision: "1", Title: &title}, "proof-edit"); err == nil {
				t.Fatal("unrelated adapter proof completed the command")
			}
			if wrongAbsence && writer.applyCalled {
				t.Fatal("unrelated absence evidence authorized Apply")
			}
		})
	}
}
