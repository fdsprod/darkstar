package workflow

import (
	"context"
	"encoding/json"
	"sort"

	"darkstar/src/core/contentlibrary"
)

// ContentUsage is a read projection over authoring documents. No mutable usage
// counter is kept alongside authoritative workflow references.
type ContentUsage struct {
	Kind            string                   `json:"kind"`
	WorkflowName    string                   `json:"workflowName"`
	WorkflowVersion string                   `json:"workflowVersion"`
	DraftID         string                   `json:"draftId,omitempty"`
	NodeID          string                   `json:"nodeId,omitempty"`
	InputID         string                   `json:"inputId,omitempty"`
	Reference       contentlibrary.Reference `json:"reference"`
}

func (c *Catalog) ContentUsages(ctx context.Context, contentID string) ([]ContentUsage, error) {
	library, err := c.Library(ctx)
	if err != nil {
		return nil, err
	}
	result := []ContentUsage{}
	for _, draft := range library.Drafts {
		result = append(result, contentDocumentUsages(draft.Document, contentID, ContentUsage{Kind: "draft", DraftID: draft.ID, WorkflowName: draft.Name})...)
	}
	for _, version := range library.Versions {
		definition, err := c.Definition(ctx, version.Name, version.Version)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(definition.Document)
		if err != nil {
			return nil, err
		}
		result = append(result, contentDocumentUsages(encoded, contentID, ContentUsage{Kind: "published", WorkflowName: version.Name, WorkflowVersion: version.Version})...)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		return left.WorkflowName+"\x00"+left.Kind+"\x00"+left.DraftID+"\x00"+left.WorkflowVersion+"\x00"+left.NodeID+"\x00"+left.InputID < right.WorkflowName+"\x00"+right.Kind+"\x00"+right.DraftID+"\x00"+right.WorkflowVersion+"\x00"+right.NodeID+"\x00"+right.InputID
	})
	return result, nil
}

func contentDocumentUsages(raw json.RawMessage, contentID string, origin ContentUsage) []ContentUsage {
	// Drafts may be temporarily incomplete, so usage reads only the known
	// reference locations without requiring a publishable workflow.
	var value struct {
		Metadata struct {
			Version string `json:"version"`
		} `json:"metadata"`
		Spec struct {
			Nodes map[string]struct {
				Prompt *contentlibrary.Reference `json:"prompt"`
			} `json:"nodes"`
			Inputs map[string]struct {
				Resource struct {
					Kind      string                    `json:"kind"`
					Reference *contentlibrary.Reference `json:"reference"`
				} `json:"resource"`
			} `json:"inputs"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	if origin.WorkflowVersion == "" {
		origin.WorkflowVersion = value.Metadata.Version
	}
	result := []ContentUsage{}
	for id, node := range value.Spec.Nodes {
		if node.Prompt != nil && node.Prompt.ID == contentID {
			usage := origin
			usage.NodeID, usage.Reference = id, *node.Prompt
			result = append(result, usage)
		}
	}
	for id, input := range value.Spec.Inputs {
		if input.Resource.Kind == "template_reference" && input.Resource.Reference != nil && input.Resource.Reference.ID == contentID {
			usage := origin
			usage.InputID, usage.Reference = id, *input.Resource.Reference
			result = append(result, usage)
		}
	}
	return result
}
