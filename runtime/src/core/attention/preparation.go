package attention

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"darkstar/src/core/preparation"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/routeadvisor"
	"darkstar/src/ports/statestore"
)

// PreparationInputRequired is a sibling of provider input, derived solely from
// a waiting run's immutable preparation assessment. It has no provider owner.
type PreparationInputRequired struct {
	Envelope
	Subject        PreparationInputSubject  `json:"subject"`
	AllowedActions []PreparationInputAction `json:"allowedActions"`
}

type PreparationInputAction string

const PreparationInputPrepare PreparationInputAction = "prepare"

type PreparationInputSubject struct {
	AssessmentDigest string                  `json:"assessmentDigest"`
	Questions        []routeadvisor.Question `json:"questions"`
}

func (subject PreparationInputSubject) MarshalJSON() ([]byte, error) {
	type fields PreparationInputSubject
	return json.Marshal(struct {
		Source string `json:"source"`
		fields
	}{Source: "route_preparation", fields: fields(subject)})
}

func (PreparationInputRequired) checkpoint()           {}
func (item PreparationInputRequired) Common() Envelope { return item.Envelope }

type preparationSource interface {
	Runs(context.Context) ([]statestore.RunProjection, error)
}

func (service *Service) preparationInputs(ctx context.Context, request ListRequest) (Checkpoints, error) {
	source, ok := service.source.(preparationSource)
	if !ok {
		return nil, nil
	}
	runs, err := source.Runs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read preparation runs: %w", err)
	}
	items := Checkpoints{}
	for _, run := range runs {
		if run.Status != statestore.RunWaiting || run.RouteSnapshot == "" {
			continue
		}
		if request.RunID != "" && request.RunID != run.RunID {
			continue
		}
		if request.WorkItemID != "" && request.WorkItemID != run.WorkItemID {
			continue
		}
		var route workflow.Route
		if err := json.Unmarshal([]byte(run.RouteSnapshot), &route); err != nil {
			return nil, fmt.Errorf("decode preparation route: %w", err)
		}
		if len(route.Assessment) == 0 {
			continue
		}
		var assessment preparation.Assessment
		if err := json.Unmarshal(route.Assessment, &assessment); err != nil {
			return nil, fmt.Errorf("decode preparation assessment: %w", err)
		}
		if err := preparation.Verify(assessment); err != nil {
			return nil, fmt.Errorf("verify preparation assessment: %w", err)
		}
		route.Assessment = nil
		if preparation.Digest(route) != preparation.Digest(assessment.Route) || assessment.Input.Work.WorkItemID != run.WorkItemID {
			return nil, fmt.Errorf("preparation assessment differs from run %s", run.RunID)
		}
		if assessment.Readiness() != "input_required" {
			continue
		}
		contextValue, urgency, err := service.context(ctx, run.RunID)
		if err != nil {
			return nil, err
		}
		if !matches(request, contextValue) {
			continue
		}
		items = append(items, PreparationInputRequired{
			Envelope:       Envelope{Kind: KindInputRequired, ID: run.RunID, Context: contextValue, Urgency: urgency, CreatedAt: run.CreatedAt, ResourceVersion: run.ResourceVersion, Summary: "Preparation input required for " + contextValue.WorkTitle, DeepLink: "/work/" + url.PathEscape(run.WorkItemID)},
			Subject:        PreparationInputSubject{AssessmentDigest: assessment.Digest, Questions: assessment.Questions},
			AllowedActions: []PreparationInputAction{PreparationInputPrepare},
		})
	}
	return items, nil
}
