package artifactderive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/core/extensions"
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/extension"
)

type catalogProcessor struct {
	contentprocessor.Processor
	name  string
	state contentprocessor.SupportState
}

func (p catalogProcessor) Descriptor() contentprocessor.Descriptor {
	return contentprocessor.Descriptor{Name: p.name, Version: "1.0.0"}
}
func (p catalogProcessor) Supports(context.Context, contentprocessor.SourceDescriptor) (contentprocessor.Support, error) {
	return contentprocessor.Support{State: p.state}, nil
}

func TestProcessorSelectionHonorsQuarantineAndExactPins(t *testing.T) {
	blocked := catalogProcessor{name: "blocked", state: contentprocessor.SupportQuarantined}
	allowed := catalogProcessor{name: "allowed", state: contentprocessor.SupportSupported}
	c, err := NewProcessorCatalog(blocked, allowed)
	if err != nil {
		t.Fatal(err)
	}
	p, s, err := c.Select(context.Background(), contentprocessor.SourceDescriptor{})
	if err != nil || p != nil || s.State != contentprocessor.SupportQuarantined {
		t.Fatal("quarantine fell through")
	}
	if _, err := NewProcessorCatalog(allowed, allowed); err == nil {
		t.Fatal("duplicate accepted")
	}
	ref := extension.Ref{ID: "example/processor", Version: "1.0.0", Digest: strings.Repeat("a", 64)}
	pinned, err := NewPinnedProcessorCatalog(extensions.Registration[contentprocessor.Processor]{Descriptor: extension.Descriptor{Ref: ref, Protocol: extension.Protocol, ConfigurationSchema: json.RawMessage(`{"type":"object"}`)}, Implementation: allowed})
	if err != nil {
		t.Fatal(err)
	}
	p, _, err = pinned.SelectPinned(context.Background(), contentprocessor.SourceDescriptor{}, &ref)
	if err != nil || p.Descriptor().Digest != ref.Digest {
		t.Fatalf("pinned selection: %v", err)
	}
	ref.Digest = strings.Repeat("b", 64)
	if _, _, err := pinned.SelectPinned(context.Background(), contentprocessor.SourceDescriptor{}, &ref); err == nil {
		t.Fatal("digest replacement accepted")
	}
}
