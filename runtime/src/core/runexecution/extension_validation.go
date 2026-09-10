package runexecution

import (
	"context"
	"encoding/json"

	"darkstar/src/core/extensions"
	"darkstar/src/core/workflow"
)

func (s *Service) validateExtensionOutputs(ctx context.Context, dispatch AttemptRequestContext, outputs map[workflow.Identifier]json.RawMessage) ([]extensions.ValidationEvidence, error) {
	checks := []extensions.Check{}
	for _, declared := range dispatch.Node.Fields().Validators {
		if check, ok := declared.(workflow.ExtensionValidator); ok {
			checks = append(checks, extensions.Check{Ref: check.Extension.Ref, Configuration: check.Extension.Configuration})
		}
	}
	if len(checks) == 0 {
		return nil, nil
	}
	validator, ok := s.requestBuilder.(interface {
		ValidateExtensions(context.Context, []extensions.Check, map[workflow.Identifier]json.RawMessage, map[workflow.Identifier]json.RawMessage) ([]extensions.ValidationEvidence, error)
	})
	if !ok {
		return nil, &workflowAdmissionError{code: "RUN_VALIDATOR_UNAVAILABLE", message: "extension validators are not configured"}
	}
	evidence, err := validator.ValidateExtensions(ctx, checks, dispatch.NodeInputs, outputs)
	if err != nil {
		return evidence, &workflowAdmissionError{code: "RUN_VALIDATOR_FAILED", message: err.Error()}
	}
	return evidence, nil
}
