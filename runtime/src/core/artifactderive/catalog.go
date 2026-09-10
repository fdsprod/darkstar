package artifactderive

import (
	"context"
	"fmt"
	"strings"

	"darkstar/src/core/extensions"
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/extension"
)

// ProcessorCatalog freezes configured precedence. Duplicate identities are an
// error, never a last-registration-wins replacement. Discovery and derivation
// use exactly the same selection path.
type ProcessorCatalog struct {
	processors []contentprocessor.Processor
	pinned     *extensions.Catalog[contentprocessor.Processor]
}

func NewProcessorCatalog(processors ...contentprocessor.Processor) (*ProcessorCatalog, error) {
	seen := map[string]bool{}
	for _, p := range processors {
		if p == nil {
			return nil, fmt.Errorf("content processors must not be nil")
		}
		d := p.Descriptor()
		if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Version) == "" {
			return nil, fmt.Errorf("processor name and version are required")
		}
		key := d.Name + "@" + d.Version
		if seen[key] {
			return nil, fmt.Errorf("duplicate content processor %s", key)
		}
		seen[key] = true
	}
	return &ProcessorCatalog{processors: append([]contentprocessor.Processor(nil), processors...)}, nil
}

// NewPinnedProcessorCatalog is the extension entry point. Configured order
// defines precedence, while an explicit request can select an exact version.
func NewPinnedProcessorCatalog(entries ...extensions.Registration[contentprocessor.Processor]) (*ProcessorCatalog, error) {
	processors := make([]contentprocessor.Processor, 0, len(entries))
	wrapped := make([]extensions.Registration[contentprocessor.Processor], 0, len(entries))
	for _, e := range entries {
		if e.Implementation == nil {
			return nil, fmt.Errorf("processor implementation is required")
		}
		if len(e.Descriptor.RequiredCapabilities) > 0 {
			return nil, fmt.Errorf("processor capabilities require a separately authorized host")
		}
		p := pinnedProcessor{Processor: e.Implementation, ref: e.Descriptor.Ref}
		processors = append(processors, p)
		wrapped = append(wrapped, extensions.Registration[contentprocessor.Processor]{Descriptor: e.Descriptor, Implementation: p})
	}
	pinned, err := extensions.New(wrapped...)
	if err != nil {
		return nil, err
	}
	catalog, err := NewProcessorCatalog(processors...)
	if err != nil {
		return nil, err
	}
	catalog.pinned = pinned
	return catalog, nil
}

type pinnedProcessor struct {
	contentprocessor.Processor
	ref extension.Ref
}

func (p pinnedProcessor) Descriptor() contentprocessor.Descriptor {
	d := p.Processor.Descriptor()
	d.Name, d.Version, d.Digest = p.ref.ID, p.ref.Version, p.ref.Digest
	return d
}

func (c *ProcessorCatalog) SelectPinned(ctx context.Context, source contentprocessor.SourceDescriptor, ref *extension.Ref) (contentprocessor.Processor, contentprocessor.Support, error) {
	if ref == nil {
		return c.Select(ctx, source)
	}
	p, err := c.pinned.Resolve(*ref)
	if err != nil {
		return nil, contentprocessor.Support{}, err
	}
	s, err := p.Supports(ctx, source)
	if err != nil {
		return nil, s, err
	}
	if s.State != contentprocessor.SupportSupported {
		return nil, s, fmt.Errorf("pinned processor %s cannot process source (%s)", ref.ID, s.State)
	}
	return p, s, nil
}

func (c *ProcessorCatalog) Select(ctx context.Context, source contentprocessor.SourceDescriptor) (contentprocessor.Processor, contentprocessor.Support, error) {
	for _, p := range c.processors {
		if err := ctx.Err(); err != nil {
			return nil, contentprocessor.Support{}, err
		}
		s, err := p.Supports(ctx, source)
		if err != nil {
			return nil, s, err
		}
		switch s.State {
		case contentprocessor.SupportSupported:
			return p, s, nil
		case contentprocessor.SupportQuarantined:
			return nil, s, nil
		case contentprocessor.SupportUnsupported:
		default:
			return nil, s, fmt.Errorf("processor %s returned invalid support state %q", p.Descriptor().Name, s.State)
		}
	}
	return nil, contentprocessor.Support{State: contentprocessor.SupportUnsupported, MediaType: source.DetectedMediaType}, nil
}
