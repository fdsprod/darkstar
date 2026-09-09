package codex

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"darkstar/src/core/workflowchat"
)

func (runner WorkflowChat) Models(ctx context.Context) ([]workflowchat.Model, error) {
	if runner.Executable == "" {
		return nil, errors.New("Codex is unavailable")
	}
	client, _, err := StartAppServer(ctx, runner.Executable, AppServerOptions{ClientInfo: ClientInfo{Name: "darkstar-workflow-chat", Title: "DARKSTAR Workflow Chat", Version: "1.0.0"}})
	if err != nil {
		return nil, err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if client.Shutdown(shutdown) != nil {
			_ = client.KillOwnedProcess()
		}
	}()
	var config struct {
		Config map[string]any `json:"config"`
	}
	if err = client.Call(ctx, "config/read", map[string]any{"includeLayers": false}, &config); err != nil {
		return nil, err
	}
	models, err := listChatModels(ctx, client)
	if err != nil {
		return nil, err
	}
	if configured, ok := config.Config["model"].(string); ok {
		if slices.ContainsFunc(models, func(m workflowchat.Model) bool { return m.ID == configured }) {
			for i := range models {
				models[i].IsDefault = models[i].ID == configured
			}
		}
	}
	if effort, ok := config.Config["model_reasoning_effort"].(string); ok {
		for i := range models {
			if models[i].IsDefault && slices.Contains(models[i].Efforts, effort) {
				models[i].DefaultEffort = effort
			}
		}
	}
	return models, nil
}

func listChatModels(ctx context.Context, client *AppServerClient) ([]workflowchat.Model, error) {
	models := []workflowchat.Model{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 20; page++ {
		var response struct {
			Data []struct {
				Model                     string `json:"model"`
				DisplayName               string `json:"displayName"`
				Hidden                    bool   `json:"hidden"`
				IsDefault                 bool   `json:"isDefault"`
				DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
				SupportedReasoningEfforts []struct {
					ReasoningEffort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := client.Call(ctx, "model/list", params, &response); err != nil {
			return nil, err
		}
		for _, m := range response.Data {
			if m.Hidden || m.Model == "" || seen[m.Model] {
				continue
			}
			efforts := []string{}
			for _, effort := range m.SupportedReasoningEfforts {
				if effort.ReasoningEffort != "" {
					efforts = append(efforts, effort.ReasoningEffort)
				}
			}
			if len(efforts) == 0 {
				continue
			}
			defaultEffort := m.DefaultReasoningEffort
			if !slices.Contains(efforts, defaultEffort) {
				defaultEffort = efforts[0]
			}
			models = append(models, workflowchat.Model{ID: m.Model, Name: m.DisplayName, Efforts: efforts, DefaultEffort: defaultEffort, IsDefault: m.IsDefault})
			seen[m.Model] = true
		}
		if response.NextCursor == nil || *response.NextCursor == "" {
			return models, nil
		}
		if *response.NextCursor == cursor {
			return nil, errors.New("model catalog pagination did not advance")
		}
		cursor = *response.NextCursor
	}
	return nil, errors.New("model catalog exceeded page limit")
}

func validateChatGeneration(models []workflowchat.Model, generation workflowchat.Generation) error {
	for _, m := range models {
		if m.ID == generation.Model {
			if slices.Contains(m.Efforts, generation.Effort) {
				return nil
			}
			return fmt.Errorf("effort %q is not supported by model %q; refresh the model picker", generation.Effort, generation.Model)
		}
	}
	return fmt.Errorf("model %q is unavailable; refresh the model picker", generation.Model)
}
