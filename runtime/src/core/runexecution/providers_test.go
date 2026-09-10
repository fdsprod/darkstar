package runexecution

import (
	"context"
	"strings"
	"testing"

	"darkstar/src/ports/extension"
	"darkstar/src/ports/provider"
)

type catalogTestProvider struct{ provider.Provider }

func TestProviderCatalogDispatchesRecordedIdentityAndRejectsReplacement(t *testing.T) {
	ref := extension.Ref{ID: "example/provider", Version: "1.0.0", Digest: strings.Repeat("a", 64)}
	selected := &catalogTestProvider{}
	var received ProviderRequest
	registration := ProviderRegistration{Name: "custom", Ref: &ref, Factory: WorkflowProviderFactoryFunc(func(_ context.Context, r ProviderRequest) (provider.Provider, error) {
		received = r
		return selected, nil
	})}
	catalog, err := NewProviderCatalog(registration)
	if err != nil {
		t.Fatal(err)
	}
	request := ProviderRequest{Provider: "custom", AttemptID: "attempt-x", Resume: true}
	actual, err := catalog.Provider(context.Background(), request)
	if err != nil || actual != selected || received != request {
		t.Fatalf("dispatch=%v request=%v err=%v", actual, received, err)
	}
	if _, err := catalog.Provider(context.Background(), ProviderRequest{Provider: "missing"}); err == nil {
		t.Fatal("unknown provider fell back")
	}
	if _, err := NewProviderCatalog(registration, registration); err == nil {
		t.Fatal("duplicate provider registered")
	}
	pins := catalog.ExtensionPins()
	ref.Digest = strings.Repeat("b", 64)
	pins["provider:custom"] = ref
	if err := catalog.ValidateExtensionPins(pins); err == nil {
		t.Fatal("replacement digest accepted")
	}
}
