package investigationrunner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"darkstar/src/core/investigation"
	"darkstar/src/ports/provider"
)

// validateRetainedRequest authenticates the frozen dispatch facts before any
// recovery action. Recovery never replaces this request with current settings.
func (s *Service) validateRetainedRequest(ctx context.Context, execution investigation.Execution, attempt investigation.Attempt) error {
	if len(attempt.Request) == 0 || digest(attempt.Request) != attempt.ContextDigest {
		return errors.New("retained provider request does not match its frozen context digest")
	}
	var request provider.AttemptRequest
	if err := json.Unmarshal(attempt.Request, &request); err != nil {
		return err
	}
	if request.AttemptID != execution.AttemptID || attempt.Handle == nil || attempt.Handle.AttemptID != execution.AttemptID || request.RunID != "" || request.NodeID != "" || request.CapabilityFingerprint != attempt.CapabilityFingerprint || request.CapabilityFingerprint != execution.Provider.CapabilityFingerprint || request.Filesystem == nil {
		return errors.New("retained provider request identity differs from the admitted attempt")
	}
	if err := validateTaskScope(execution); err != nil {
		return err
	}
	if len(request.Inputs) != 2 || request.Inputs[0].Name != "task" || request.Inputs[0].Text != string(execution.Task.Content) || request.Inputs[0].Digest != execution.Task.Digest {
		return errors.New("retained provider task differs from frozen task bytes")
	}
	roots := []string{}
	configs := []string{}
	for _, entry := range execution.Scope.Repositories {
		configs = append(configs, entry.ConfigurationDigest)
	}
	for _, evidence := range execution.Scope.Evidence {
		if err := s.dependencies.Evidence.Verify(ctx, evidence.Evidence); err != nil {
			return err
		}
		roots = append(roots, evidence.Evidence.Root)
	}
	encoded, _ := json.Marshal(configs)
	expected := provider.ScopedReadRequirement{ReadRoots: roots, ScopeDigest: execution.Scope.Binding.ScopeDigest, ConfigurationDigest: digest(encoded), EvidenceDigest: execution.Scope.Binding.EvidenceDigest}
	if !reflect.DeepEqual(*request.Filesystem, expected) || request.Access != provider.AccessReadOnly || request.Network != provider.NetworkDenied || request.CommandPolicy != provider.InteractionDeny || request.FilePolicy != provider.InteractionDeny || len(request.AdditionalRoots) != 0 {
		return errors.New("retained provider filesystem or permission ceiling changed")
	}
	if request.Inputs[1].Digest != digest([]byte(request.Inputs[1].Text)) {
		return errors.New("retained evidence input content digest changed")
	}
	return nil
}
