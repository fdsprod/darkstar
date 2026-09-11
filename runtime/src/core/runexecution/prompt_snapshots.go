package runexecution

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/contentstore"
)

type publishedContentResolver interface {
	ResolveContent(context.Context, contentstore.Reference) (contentstore.Version, error)
}

func resolvePromptSnapshots(ctx context.Context, planner WorkflowPlanner, document workflow.Document) (map[string]contentstore.Version, error) {
	result := map[string]contentstore.Version{}
	ids := make([]string, 0, len(document.Spec.Nodes))
	for id := range document.Spec.Nodes {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, id := range ids {
		reference := document.Spec.Nodes[workflow.Identifier(id)].Fields().Prompt
		if reference == nil {
			continue
		}
		resolver, ok := planner.(publishedContentResolver)
		if !ok {
			return nil, errors.New("linked prompt requires a content resolver")
		}
		version, err := resolver.ResolveContent(ctx, *reference)
		if err != nil {
			return nil, fmt.Errorf("resolve linked prompt for %s: %w", id, err)
		}
		if version.Document.Kind != "prompt" {
			return nil, fmt.Errorf("linked prompt for %s references a different content kind", id)
		}
		result[id] = version
	}
	return result, nil
}
