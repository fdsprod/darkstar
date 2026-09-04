package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/runexecution"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

var permissionIdentityPattern = regexp.MustCompile(`^permission_[0-9A-HJKMNP-TV-Z]{26}$`)

func runAgentPermissions(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar agent permissions"
	if len(args) == 0 {
		return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "a permissions command is required (list, show, decide, retry)", false, ExitInvalidInput)
	}
	command += " " + args[0]
	switch args[0] {
	case "list":
		attemptID, status, err := parsePermissionList(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		query := url.Values{}
		if attemptID != "" {
			query.Set("attemptId", attemptID)
		}
		if status != "" {
			query.Set("status", string(status))
		}
		path := "agents/permissions"
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var list runexecution.ProviderPermissionList
		if err := session.DoJSON(context.Background(), http.MethodGet, path, nil, &list); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		if jsonOutput {
			return writeAgentMachineResult(list, stdout, stderr, command)
		}
		for _, item := range list.Items {
			_, _ = fmt.Fprintf(stdout, "%s %s attempt=%s kind=%s summary=%s actions=%s\n", item.ID, item.Status, item.AttemptID, item.InteractionKind, permissionSummary(item), joinPermissionActions(item.AllowedActions))
		}
		return int(ExitSuccess)
	case "show":
		if len(args) != 2 || !permissionIdentityPattern.MatchString(args[1]) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "expected 'agent permissions show <permission-id>'", false, ExitInvalidInput)
		}
		return getAndWriteProviderPermission(command, args[1], jsonOutput, stdout, stderr)
	case "decide":
		id, decision, key, err := parsePermissionDecision(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var view runexecution.ProviderPermissionView
		if err := session.DoJSON(context.Background(), http.MethodGet, "agents/permissions/"+id, nil, &view); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		if !slices.Contains(view.AllowedActions, runexecution.ProviderPermissionAction(decision)) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "PROVIDER_PERMISSION_CONFLICT", "the selected provider permission decision is not currently allowed", false, ExitConflict)
		}
		body := struct {
			Decision    provider.PermissionDecision `json:"decision"`
			ScopeDigest string                      `json:"scopeDigest"`
		}{decision, view.ScopeDigest}
		if err := session.DoJSON(context.Background(), http.MethodPost, "agents/permissions/"+id+"/decisions", body, &view, clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, view.ResourceVersion))); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeProviderPermission(view, jsonOutput, stdout, stderr, command)
	case "retry":
		if len(args) != 2 || !permissionIdentityPattern.MatchString(args[1]) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "expected 'agent permissions retry <permission-id>'", false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var view runexecution.ProviderPermissionView
		if err := session.DoJSON(context.Background(), http.MethodGet, "agents/permissions/"+args[1], nil, &view); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		if !slices.Contains(view.AllowedActions, runexecution.ProviderPermissionRetryDelivery) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "PROVIDER_PERMISSION_CONFLICT", "provider permission delivery retry is not currently allowed", false, ExitConflict)
		}
		if err := session.DoJSON(context.Background(), http.MethodPost, "agents/permissions/"+args[1]+"/delivery-retries", nil, &view, clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, view.ResourceVersion))); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeProviderPermission(view, jsonOutput, stdout, stderr, command)
	default:
		return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", fmt.Sprintf("unknown permissions command %q", args[0]), false, ExitInvalidInput)
	}
}

func parsePermissionList(args []string) (string, statestore.ProviderPermissionStatus, error) {
	var attemptID string
	var status statestore.ProviderPermissionStatus
	for index := 0; index < len(args); index += 2 {
		if index+1 >= len(args) {
			return "", "", errors.New("permissions list flags require a value")
		}
		switch args[index] {
		case "--attempt":
			if attemptID != "" || !attemptIdentityPattern.MatchString(args[index+1]) {
				return "", "", errors.New("--attempt requires one canonical attempt_ ULID")
			}
			attemptID = args[index+1]
		case "--status":
			if status != "" {
				return "", "", errors.New("--status may be supplied once")
			}
			status = statestore.ProviderPermissionStatus(args[index+1])
			if status != statestore.ProviderPermissionPending && status != statestore.ProviderPermissionDecisionRecorded && status != statestore.ProviderPermissionResponded {
				return "", "", errors.New("--status must be pending, decision_recorded, or responded")
			}
		default:
			return "", "", fmt.Errorf("unknown permissions list flag %q", args[index])
		}
	}
	return attemptID, status, nil
}

func parsePermissionDecision(args []string) (string, provider.PermissionDecision, string, error) {
	if len(args) != 2 && len(args) != 4 {
		return "", "", "", errors.New("expected 'agent permissions decide <permission-id> <allow_once|deny|cancel> [--idempotency-key <key>]' ")
	}
	if !permissionIdentityPattern.MatchString(args[0]) {
		return "", "", "", errors.New("permission decision requires a canonical permission_ ULID")
	}
	decision := provider.PermissionDecision(args[1])
	if decision != provider.PermissionAllowOnce && decision != provider.PermissionDenied && decision != provider.PermissionCancelled {
		return "", "", "", errors.New("decision must be allow_once, deny, or cancel")
	}
	key := newIdempotencyKey()
	if len(args) == 4 {
		if args[2] != "--idempotency-key" || strings.TrimSpace(args[3]) == "" {
			return "", "", "", errors.New("--idempotency-key requires a non-empty value")
		}
		key = args[3]
	}
	return args[0], decision, key, nil
}

func getAndWriteProviderPermission(command, id string, jsonOutput bool, stdout, stderr io.Writer) int {
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var view runexecution.ProviderPermissionView
	if err := session.DoJSON(context.Background(), http.MethodGet, "agents/permissions/"+id, nil, &view); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeProviderPermission(view, jsonOutput, stdout, stderr, command)
}

func writeProviderPermission(view runexecution.ProviderPermissionView, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		return writeAgentMachineResult(view, stdout, stderr, command)
	}
	_, _ = fmt.Fprintf(stdout, "%s %s\nAttempt: %s\nInteraction: %s\nProvider thread: %s\nProvider turn: %s\nProvider request: %s\nTarget: %s\nOperation: %s\nSubject: %s\nScope digest: %s\nPolicy digest: %s\nSummary: %s\nAllowed actions: %s\nWarning: allow_once applies only to this recorded provider interaction; it is not workflow approval.\n", view.ID, view.Status, view.AttemptID, view.InteractionKind, view.ProviderThreadID, view.ProviderTurnID, view.ProviderRequestID, view.Scope.Target, view.Scope.Operation, view.Scope.Subject, view.ScopeDigest, view.PolicyDigest, permissionSummary(view), joinPermissionActions(view.AllowedActions))
	return int(ExitSuccess)
}

func writeAgentMachineResult(value any, stdout, stderr io.Writer, command string) int {
	if err := writeJSON(stdout, agentMachineOutput{SchemaVersion: machineSchemaVersion, Result: value}); err != nil {
		return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
	}
	return int(ExitSuccess)
}

func permissionSummary(view runexecution.ProviderPermissionView) string {
	var evidence struct {
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal([]byte(view.Evidence), &evidence)
	return evidence.Summary
}

func joinPermissionActions(actions []runexecution.ProviderPermissionAction) string {
	values := make([]string, len(actions))
	for index := range actions {
		values[index] = string(actions[index])
	}
	return strings.Join(values, ", ")
}
