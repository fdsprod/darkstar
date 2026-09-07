package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"darkstar/src/core/config"
	"darkstar/src/core/preparation"
	"darkstar/src/core/workflow"
	daemonconfiguration "darkstar/src/daemon/configuration"
	platformport "darkstar/src/ports/platform"
)

// configuredPreparationPolicy reloads effective configuration for each immutable
// assessment so later configuration edits cannot mutate a recorded decision.
func configuredPreparationPolicy(paths platformport.Paths, projectRoot string) (preparation.Policy, error) {
	policy := preparation.Policy{Version: "smallest-safe-v1", RequiredNodes: []workflow.Identifier{}, ConsequentialNodes: []workflow.Identifier{}, AllowedAssumptions: []string{}}
	locations, err := daemonconfiguration.ResolveFileLocations(paths, projectRoot)
	if err != nil {
		return policy, err
	}
	defaults, err := config.Defaults(map[string]any{})
	if err != nil {
		return policy, err
	}
	effective, err := daemonconfiguration.Resolve(defaults, locations)
	if err != nil {
		return policy, fmt.Errorf("resolve route assessment policy: %w", err)
	}

	routingValue, found := effective.Values()["routing"]
	if !found {
		return policy, nil
	}
	routing, ok := routingValue.(map[string]any)
	if !ok {
		return policy, errors.New("routing must be an object")
	}
	setting, found := routing["assessment"]
	if !found {
		return policy, nil
	}
	fields, ok := setting.(map[string]any)
	if !ok {
		return policy, errors.New("routing.assessment must be an object")
	}
	for key, value := range fields {
		if value == nil {
			return policy, fmt.Errorf("routing.assessment.%s must not be null", key)
		}
	}
	content, err := json.Marshal(fields)
	if err != nil {
		return policy, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("invalid routing.assessment: %w", err)
	}
	if strings.TrimSpace(policy.Version) == "" {
		return policy, errors.New("routing.assessment.version must be non-empty")
	}
	for _, nodes := range [][]workflow.Identifier{policy.RequiredNodes, policy.ConsequentialNodes} {
		seen := map[workflow.Identifier]bool{}
		for _, id := range nodes {
			if !runNodePattern.MatchString(string(id)) || seen[id] {
				return policy, fmt.Errorf("routing.assessment requires unique valid node identifiers: %q", id)
			}
			seen[id] = true
		}
	}
	seen := map[string]bool{}
	for _, value := range policy.AllowedAssumptions {
		if strings.TrimSpace(value) == "" || seen[value] {
			return policy, errors.New("routing.assessment.allowedAssumptions requires unique non-empty strings")
		}
		seen[value] = true
	}
	return policy, nil
}
