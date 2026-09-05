package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
)

const maxSecretInput = 64 << 10

func runConfiguration(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar configuration", "ARGUMENT_INVALID", "a command is required (catalog, state, preview, set, unset, restore, secret-set)", false, ExitInvalidInput)
	}
	command := "darkstar configuration " + args[0]
	switch args[0] {
	case "catalog":
		if len(args) != 1 {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "catalog accepts no arguments", false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result config.Catalog
		if err := session.DoJSON(context.Background(), http.MethodGet, "configuration/catalog", nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeConfigurationResult(result, jsonOutput, stdout, stderr, command)
	case "state":
		scope, err := parseConfigurationScope(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		resource := "configuration/state"
		if scope.Kind() == config.MutationScopeProject {
			resource += "?projectId=" + url.QueryEscape(scope.ProjectID())
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result configmutation.State
		if err := session.DoJSON(context.Background(), http.MethodGet, resource, nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeConfigurationResult(result, jsonOutput, stdout, stderr, command)
	case "preview", "set", "unset":
		request, key, err := parseConfigurationMutation(args)
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		if args[0] == "preview" {
			var result configmutation.Preview
			if err := session.DoJSON(context.Background(), http.MethodPost, "configuration/preview", request, &result); err != nil {
				return writeClientError(stdout, stderr, jsonOutput, command, err)
			}
			return writeConfigurationResult(result, jsonOutput, stdout, stderr, command)
		}
		var result configmutation.ApplyResult
		if err := session.DoJSON(context.Background(), http.MethodPost, "configuration/apply", request, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeConfigurationResult(result, jsonOutput, stdout, stderr, command)
	case "restore":
		scope, revision, key, err := parseRestore(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		body := struct {
			Scope            config.MutationScope `json:"scope"`
			ExpectedRevision string               `json:"expectedRevision"`
		}{scope, revision}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result configmutation.ApplyResult
		if err := session.DoJSON(context.Background(), http.MethodPost, "configuration/restore", body, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeConfigurationResult(result, jsonOutput, stdout, stderr, command)
	case "secret-set":
		request, key, err := parseSecretWrite(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		body := struct {
			Name             string `json:"name"`
			Value            string `json:"value"`
			ExpectedRevision string `json:"expectedRevision"`
		}{request.Name, request.Value, request.ExpectedRevision}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result configmutation.SecretReceipt
		if err := session.DoJSON(context.Background(), http.MethodPost, "configuration/secrets", body, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeConfigurationResult(result, jsonOutput, stdout, stderr, command)
	default:
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar configuration", "ARGUMENT_INVALID", fmt.Sprintf("unknown configuration command %q", args[0]), false, ExitInvalidInput)
	}
}

func parseConfigurationScope(args []string) (config.MutationScope, error) {
	if len(args) == 0 {
		return config.UserMutationScope(), nil
	}
	if len(args) == 2 && args[0] == "--project" {
		return config.ProjectMutationScope(args[1])
	}
	return config.MutationScope{}, errors.New("expected only optional --project <project-id>")
}

func parseConfigurationMutation(args []string) (configmutation.MutationRequest, string, error) {
	operation := args[0]
	var key, value, valueType, revision, projectID, idempotency string
	unset := operation == "unset"
	for index := 1; index < len(args); index++ {
		arg := args[index]
		if arg == "--unset" {
			if unset {
				return configmutation.MutationRequest{}, "", errors.New("--unset may be specified once")
			}
			unset = true
			continue
		}
		if index+1 >= len(args) {
			return configmutation.MutationRequest{}, "", fmt.Errorf("%s requires a value", arg)
		}
		candidate := args[index+1]
		index++
		switch arg {
		case "--key":
			key = candidate
		case "--value":
			value = candidate
		case "--value-type":
			valueType = candidate
		case "--revision":
			revision = candidate
		case "--project":
			projectID = candidate
		case "--idempotency-key":
			idempotency = candidate
		default:
			return configmutation.MutationRequest{}, "", fmt.Errorf("unknown option %q", arg)
		}
	}
	if key == "" || revision == "" {
		return configmutation.MutationRequest{}, "", errors.New("--key and --revision are required")
	}
	scope := config.UserMutationScope()
	if projectID != "" {
		var err error
		scope, err = config.ProjectMutationScope(projectID)
		if err != nil {
			return configmutation.MutationRequest{}, "", err
		}
	}
	var change configmutation.SettingChange
	if unset {
		if value != "" || valueType != "" {
			return configmutation.MutationRequest{}, "", errors.New("unset cannot include a value")
		}
		change = configmutation.Unset()
	} else {
		if valueType == "" {
			return configmutation.MutationRequest{}, "", errors.New("--value-type is required")
		}
		typed, err := parseConfigurationValue(config.SettingType(valueType), value)
		if err != nil {
			return configmutation.MutationRequest{}, "", err
		}
		change = configmutation.Set(typed)
	}
	if operation == "preview" && idempotency != "" {
		return configmutation.MutationRequest{}, "", errors.New("preview does not accept --idempotency-key")
	}
	if idempotency == "" {
		idempotency = newIdempotencyKey()
	}
	return configmutation.MutationRequest{Scope: scope, Key: key, Change: change, ExpectedRevision: revision}, idempotency, nil
}

func parseConfigurationValue(kind config.SettingType, value string) (config.TypedValue, error) {
	switch kind {
	case config.SettingString:
		return config.StringValue(value), nil
	case config.SettingEnum:
		return config.EnumValue(value), nil
	case config.SettingPath:
		return config.PathValue(value), nil
	case config.SettingSecretReference:
		return config.SecretReferenceValue(value), nil
	case config.SettingBoolean:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return config.TypedValue{}, errors.New("boolean value must be true or false")
		}
		return config.BooleanValue(parsed), nil
	case config.SettingInteger:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return config.TypedValue{}, errors.New("integer value is invalid")
		}
		return config.IntegerValue(parsed), nil
	default:
		return config.TypedValue{}, fmt.Errorf("unknown value type %q", kind)
	}
}

func parseRestore(args []string) (config.MutationScope, string, string, error) {
	var revision, projectID, key string
	for index := 0; index < len(args); index++ {
		if index+1 >= len(args) {
			return config.MutationScope{}, "", "", fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--revision":
			revision = value
		case "--project":
			projectID = value
		case "--idempotency-key":
			key = value
		default:
			return config.MutationScope{}, "", "", fmt.Errorf("unknown option %q", args[index])
		}
		index++
	}
	if revision == "" {
		return config.MutationScope{}, "", "", errors.New("--revision is required")
	}
	scope := config.UserMutationScope()
	if projectID != "" {
		var err error
		scope, err = config.ProjectMutationScope(projectID)
		if err != nil {
			return config.MutationScope{}, "", "", err
		}
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return scope, revision, key, nil
}

func parseSecretWrite(args []string) (configmutation.SecretWriteRequest, string, error) {
	if len(args) < 1 {
		return configmutation.SecretWriteRequest{}, "", errors.New("secret name is required")
	}
	name := args[0]
	var file, revision, key string
	stdin := false
	for index := 1; index < len(args); index++ {
		if args[index] == "--stdin" {
			stdin = true
			continue
		}
		if index+1 >= len(args) {
			return configmutation.SecretWriteRequest{}, "", fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--file":
			file = value
		case "--revision":
			revision = value
		case "--idempotency-key":
			key = value
		default:
			return configmutation.SecretWriteRequest{}, "", fmt.Errorf("unknown option %q", args[index])
		}
		index++
	}
	if revision == "" || ((file == "") == stdin) {
		return configmutation.SecretWriteRequest{}, "", errors.New("--revision and exactly one of --file or --stdin are required")
	}
	var reader io.Reader = os.Stdin
	if file != "" {
		opened, err := os.Open(file)
		if err != nil {
			return configmutation.SecretWriteRequest{}, "", fmt.Errorf("open secret input: %w", err)
		}
		defer opened.Close()
		reader = opened
	}
	content, err := io.ReadAll(io.LimitReader(reader, maxSecretInput+1))
	if err != nil {
		return configmutation.SecretWriteRequest{}, "", err
	}
	if len(content) > maxSecretInput {
		return configmutation.SecretWriteRequest{}, "", errors.New("secret input exceeds 64 KiB")
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return configmutation.SecretWriteRequest{Name: name, Value: string(content), ExpectedRevision: revision}, key, nil
}

func writeConfigurationResult(value any, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, value); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
		return int(ExitSuccess)
	}
	switch result := value.(type) {
	case config.Catalog:
		for _, setting := range result.Settings {
			_, _ = fmt.Fprintf(stdout, "%s (%s, %s, restart: %s)\n", setting.Key, setting.Type, setting.Sensitivity, setting.Restart)
		}
	case configmutation.State:
		_, _ = fmt.Fprintf(stdout, "Configuration %s revision %s (%d configured, %d effective).\n", result.Scope.Kind(), result.Revision, len(result.Configured), len(result.Effective))
	case configmutation.Preview:
		_, _ = fmt.Fprintf(stdout, "Preview valid; revision %s -> %s (restart: %s).\n", result.Before.Revision, result.After.Revision, result.Restart)
	case configmutation.ApplyResult:
		_, _ = fmt.Fprintf(stdout, "Configuration revision %s applied (restart: %s).\n", result.State.Revision, result.Restart)
	case configmutation.SecretReceipt:
		_, _ = fmt.Fprintf(stdout, "Secret %s updated at revision %s (value redacted; restart: %s).\n", result.Name, result.Revision, result.Restart)
	}
	return int(ExitSuccess)
}
