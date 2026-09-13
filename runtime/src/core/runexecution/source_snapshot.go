package runexecution

import (
	"context"
	"errors"
	"fmt"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

// approvedSourceWork projects one explicitly approved, immutable observation for
// route assessment. It never resolves a provider or rewrites the intake record.
func (s *Service) approvedSourceWork(ctx context.Context, request CreateRequest, work *statestore.WorkItemProjection) (*statestore.RunSourceSnapshot, bool, error) {
	store, ok := s.store.(statestore.TicketExecutionStore)
	if !ok {
		if request.SourceObservationID != "" {
			return nil, false, fmt.Errorf("%w: source admission storage is unavailable", ErrInvalidRequest)
		}
		return nil, false, nil
	}
	lineage, err := store.WorkTicketLineage(ctx, work.WorkItemID)
	if sourceStateNotFound(err) {
		if request.SourceObservationID != "" {
			return nil, false, fmt.Errorf("%w: work has no admitted source lineage", ErrInvalidRequest)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var admitted bool
	switch lineage.Origin {
	case statestore.SourceAdmitted, statestore.SourceNative:
		admitted = true
	case statestore.SourceLegacyNative:
	default:
		return nil, false, errors.New("unsupported source lineage origin")
	}
	if request.SourceObservationID == "" {
		if !admitted {
			_, approvalErr := store.LatestTicketAdmission(ctx, work.WorkItemID)
			admitted = approvalErr == nil
			if approvalErr != nil && !sourceStateNotFound(approvalErr) {
				return nil, false, approvalErr
			}
		}
		if admitted {
			return nil, true, fmt.Errorf("%w: sourceObservationId must name the approved source version", ErrInvalidRequest)
		}
		return nil, false, nil
	}
	snapshot, err := store.ApprovedRunSource(ctx, work.WorkItemID, request.SourceObservationID)
	if err != nil {
		return nil, admitted, fmt.Errorf("%w: approved source observation is unavailable: %v", ErrInvalidRequest, err)
	}
	if snapshot.WorkID != work.WorkItemID || snapshot.Ref != lineage.Ref || snapshot.LineageRevision != lineage.Revision || snapshot.BindingRevision != lineage.BindingRevision {
		return nil, admitted, fmt.Errorf("%w: source approval does not match work lineage", ErrInvalidRequest)
	}
	if err := applySourceWork(work, snapshot); err != nil {
		return nil, admitted, err
	}
	return &snapshot, true, nil
}

func applySourceWork(work *statestore.WorkItemProjection, snapshot statestore.RunSourceSnapshot) error {
	ticket, err := trackercontract.DecodeTicket(snapshot.Ticket)
	if err != nil {
		return fmt.Errorf("decode approved source: %w", err)
	}
	if ticket.Ref != snapshot.Ref {
		return errors.New("approved source ticket identity mismatch")
	}
	_, _, digest, err := trackercontract.ObservationIdentity(ticket)
	if err != nil {
		return err
	}
	work.Title, work.Details, work.SourceHash = ticket.Title, ticket.Description, digest
	evidence := make([]string, 0, len(work.Evidence)+1)
	seen := map[string]bool{}
	for _, reference := range append(append([]string(nil), work.Evidence...), ticket.EvidenceRef) {
		if reference != "" && !seen[reference] {
			evidence = append(evidence, reference)
			seen[reference] = true
		}
	}
	work.Evidence = evidence
	return nil
}

// Source content is task data. Runtime record IDs and the persistence envelope
// remain in the durable snapshot instead of becoming model instructions.
func sourceTaskContent(snapshot statestore.RunSourceSnapshot) (any, error) {
	ticket, err := trackercontract.DecodeTicket(snapshot.Ticket)
	if err != nil {
		return nil, err
	}
	content := map[string]any{"title": ticket.Title, "details": ticket.Description, "key": ticket.Key, "url": ticket.URL}
	if state, ok := ticket.BusinessState.(tracker.Known[tracker.NamedID]); ok {
		content["businessState"] = state.Value.Name
	}
	if priority, ok := ticket.Priority.(tracker.Known[tracker.NamedID]); ok {
		content["priority"] = priority.Value.Name
	}
	return content, nil
}

func (s *Service) frozenSourceWork(ctx context.Context, run statestore.RunProjection, work *statestore.WorkItemProjection) (bool, error) {
	store, ok := s.store.(statestore.TicketExecutionStore)
	if !ok {
		return false, nil
	}
	snapshot, err := store.RunSourceSnapshot(ctx, run.RunID)
	if sourceStateNotFound(err) {
		lineage, lineageErr := store.WorkTicketLineage(ctx, work.WorkItemID)
		if lineageErr == nil && (lineage.Origin == statestore.SourceAdmitted || lineage.Origin == statestore.SourceNative) && !run.CreatedAt.Before(lineage.CreatedAt) {
			return false, errors.New("admitted run has no immutable source snapshot")
		}
		if lineageErr != nil && !sourceStateNotFound(lineageErr) {
			return false, lineageErr
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if snapshot.RunID != run.RunID || snapshot.WorkID != work.WorkItemID {
		return false, errors.New("run source snapshot ownership mismatch")
	}
	return true, applySourceWork(work, snapshot)
}

func sourceStateNotFound(err error) bool {
	var failure *ports.Failure
	return errors.Is(err, statestore.ErrNotFound) || (errors.As(err, &failure) && failure.Code == ports.FailureNotFound)
}
