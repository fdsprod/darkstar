package workflow

import (
	"context"
	"errors"
	"fmt"

	"darkstar/src/core/contentlibrary"
	"darkstar/src/ports/contentstore"
)

type ContentResolver interface {
	Resolve(context.Context, contentstore.Reference) (contentstore.Version, error)
}

func (c *Catalog) WithContentResolver(resolver ContentResolver) *Catalog {
	c.content = resolver
	return c
}

func (c *Catalog) ResolveContent(ctx context.Context, reference contentstore.Reference) (contentstore.Version, error) {
	if c.content == nil {
		return contentstore.Version{}, errors.New("content library is unavailable")
	}
	version, err := c.content.Resolve(ctx, reference)
	if err != nil {
		return contentstore.Version{}, err
	}
	if version.Reference != reference || contentlibrary.Digest(version.Document) != reference.Digest {
		return contentstore.Version{}, errors.New("published content does not match the pinned reference")
	}
	return version, nil
}

func (c *Catalog) validateContentReferences(ctx context.Context, document Document) ValidationErrors {
	issues := ValidationErrors{}
	for _, id := range sortedNodeIDs(document.Spec.Nodes) {
		ref := document.Spec.Nodes[id].Fields().Prompt
		if ref == nil {
			continue
		}
		version, err := c.ResolveContent(ctx, *ref)
		if err != nil || version.Document.Kind != "prompt" {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: "exact published prompt is unavailable or has the wrong kind", Location: fmt.Sprintf("/spec/nodes/%s/prompt", id)})
		}
	}
	for id, declaration := range document.Spec.Inputs {
		if declaration.Resource == nil {
			continue
		}
		ref, ok := declaration.Resource.Source.(TemplateReferenceResource)
		if !ok {
			continue
		}
		version, err := c.ResolveContent(ctx, ref.Reference)
		if err != nil || version.Document.Kind != "template" {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: "exact published template is unavailable or has the wrong kind", Location: fmt.Sprintf("/spec/inputs/%s/resource/reference", id)})
		}
	}
	return issues
}
