package runexecution

import (
	"context"
	"darkstar/src/ports/statestore"
	"errors"
	"fmt"
	"strings"

	"darkstar/src/ports/extension"
	"darkstar/src/ports/provider"
)

// ProviderRegistration supplies one normalized provider lifecycle. Factories
// receive attempt identity and recovery disposition, never the workflow graph.
type ProviderRegistration struct {
	Name    string
	Ref     *extension.Ref
	Factory WorkflowProviderFactory
}

type ProviderCatalog struct {
	factories map[string]WorkflowProviderFactory
	pins      map[string]extension.Ref
}

func NewProviderCatalog(registrations ...ProviderRegistration) (*ProviderCatalog, error) {
	c := &ProviderCatalog{factories: map[string]WorkflowProviderFactory{}, pins: map[string]extension.Ref{}}
	for _, r := range registrations {
		if strings.TrimSpace(r.Name) != r.Name || r.Name == "" || r.Factory == nil {
			return nil, fmt.Errorf("provider name and factory are required")
		}
		if _, exists := c.factories[r.Name]; exists {
			return nil, fmt.Errorf("duplicate provider %q", r.Name)
		}
		if r.Ref != nil {
			if err := r.Ref.Validate(); err != nil {
				return nil, err
			}
			c.pins[r.Name] = *r.Ref
		}
		c.factories[r.Name] = r.Factory
	}
	return c, nil
}

func (c *ProviderCatalog) Provider(ctx context.Context, request ProviderRequest) (provider.Provider, error) {
	if c == nil {
		return nil, fmt.Errorf("provider catalog is unavailable")
	}
	f, exists := c.factories[request.Provider]
	if !exists {
		return nil, fmt.Errorf("unsupported durable provider %q", request.Provider)
	}
	if request.Ref != nil {
		if ref, ok := c.pins[request.Provider]; !ok || ref != *request.Ref {
			return nil, fmt.Errorf("EXTENSION_UNAVAILABLE: exact provider implementation is not registered")
		}
	}
	return f.Provider(ctx, request)
}

// DefaultWorkflowProvider is configured at composition, then recorded on the
// attempt. Recovery always selects the recorded provider.
type DefaultWorkflowProvider interface{ DefaultWorkflowProvider() string }

func (s *Service) workflowProviderName() string {
	if configured, ok := s.workflowFactory.(DefaultWorkflowProvider); ok {
		return configured.DefaultWorkflowProvider()
	}
	return ProviderCodex // legacy factory compatibility
}

func (c *ProviderCatalog) ExtensionPins() map[string]extension.Ref {
	pins := map[string]extension.Ref{}
	for name, ref := range c.pins {
		pins["provider:"+name] = ref
	}
	return pins
}
func (c *ProviderCatalog) ValidateExtensionPins(pins map[string]extension.Ref) error {
	for name, ref := range pins {
		if !strings.HasPrefix(name, "provider:") {
			continue
		}
		actual, ok := c.pins[strings.TrimPrefix(name, "provider:")]
		if !ok || actual != ref {
			return fmt.Errorf("EXTENSION_UNAVAILABLE: exact provider %s@%s (%s) is required", ref.ID, ref.Version, ref.Digest)
		}
	}
	return nil
}

type ExtensionPinProvider interface {
	ExtensionPins() map[string]extension.Ref
	ValidateExtensionPins(map[string]extension.Ref) error
}

func (s *Service) workflowProviderForRun(ctx context.Context, runID string) string {
	snapshot, err := s.store.RunExecutionContext(ctx, runID)
	if err == nil && snapshot.Provider != "" {
		return snapshot.Provider
	}
	if err != nil && !errors.Is(err, statestore.ErrNotFound) {
		return ""
	} // event validation fails closed
	return s.workflowProviderName()
}
