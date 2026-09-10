package workspace_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/workmanagement"
)

type provisioner struct {
	fail  bool
	calls []string
}

func (p *provisioner) Ensure(_ context.Context, workID string) error {
	p.calls = append(p.calls, workID)
	if p.fail {
		return errors.New("disk unavailable")
	}
	return nil
}

func TestCreateRecoversProvisioningFailureAndReconcilesExistingWork(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &provisioner{fail: true}
	s, err := workmanagement.NewWithWorkspaces(db, p)
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "project", Source: "repository"}, "project-command")
	if err != nil {
		t.Fatal(err)
	}
	request := workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "work"}
	first, err := s.CreateWork(ctx, request, "create-command")
	if err == nil {
		t.Fatal("creation succeeded without workspace")
	}
	p.fail = false
	recovered, err := s.CreateWork(ctx, request, "create-command")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.WorkItemID != first.WorkItemID {
		t.Fatal("recovery created a new work identity")
	}
	if _, err := s.CreateWork(ctx, request, "create-command"); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 3 {
		t.Fatalf("provision calls = %d; completed replay must provision too", len(p.calls))
	}
	items, err := db.WorkItems(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("work items %v: %v", items, err)
	}
	if err := s.ReconcileWorkspaces(ctx); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 4 || p.calls[3] != recovered.WorkItemID {
		t.Fatalf("reconciliation = %v", p.calls)
	}
	p.fail = true
	if err := s.ReconcileWorkspaces(ctx); err == nil {
		t.Fatal("startup ignored workspace failure")
	}
}
