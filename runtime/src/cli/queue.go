package cli

import (
	"darkstar/src/core/config"
	daemonconfiguration "darkstar/src/daemon/configuration"
	platformport "darkstar/src/ports/platform"
	"encoding/json"
	"fmt"
)

func configuredQueueLimit(paths platformport.Paths, projectRoot string) (int, error) {
	locations, err := daemonconfiguration.ResolveFileLocations(paths, projectRoot)
	if err != nil {
		return 0, err
	}
	layer, found, err := daemonconfiguration.LoadOptionalFile(config.ScopeUser, locations.UserConfig)
	if err != nil {
		return 0, err
	}
	defaults, _ := config.Defaults(map[string]any{"scheduler": map[string]any{"maxConcurrentRuns": 3}})
	layers := []config.Layer{}
	if found {
		layers = append(layers, layer)
	}
	effective, err := config.Resolve(defaults, layers...)
	if err != nil {
		return 0, err
	}
	data, err := json.Marshal(effective.Values()["scheduler"])
	if err != nil {
		return 0, err
	}
	var value struct {
		MaxConcurrentRuns int `json:"maxConcurrentRuns"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	if value.MaxConcurrentRuns < 1 || value.MaxConcurrentRuns > 32 {
		return 0, fmt.Errorf("scheduler.maxConcurrentRuns must be between 1 and 32")
	}
	return value.MaxConcurrentRuns, nil
}
