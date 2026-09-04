package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/runexecution"
)

var attemptIdentityPattern = regexp.MustCompile(`^attempt_[0-9A-HJKMNP-TV-Z]{26}$`)

type agentMachineOutput struct {
	SchemaVersion int `json:"schemaVersion"`
	Result        any `json:"result"`
}

type agentLogOutput struct {
	SchemaVersion int    `json:"schemaVersion"`
	AttemptID     string `json:"attemptId"`
	Offset        int64  `json:"offset"`
	NextOffset    int64  `json:"nextOffset"`
	Complete      bool   `json:"complete"`
	Content       string `json:"content"`
}

func runAgent(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar agent", "ARGUMENT_INVALID", "an agent command is required (list, status, logs, cancel, permissions)", false, ExitInvalidInput)
	}
	command := "darkstar agent " + args[0]
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "agent list accepts no arguments", false, ExitInvalidInput)
		}
		return listAgents(command, jsonOutput, stdout, stderr)
	case "status":
		if len(args) == 1 {
			return listAgents(command, jsonOutput, stdout, stderr)
		}
		if len(args) != 2 || !attemptIdentityPattern.MatchString(args[1]) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "expected 'agent status [<attempt-id>]' with a canonical attempt_ ULID", false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var agent runexecution.Agent
		if err := session.DoJSON(context.Background(), http.MethodGet, "agents/"+args[1], nil, &agent); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeAgent(agent, jsonOutput, stdout, stderr, command)
	case "logs":
		attemptID, follow, err := parseAgentLogs(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		return followAgentLog(command, attemptID, follow, jsonOutput, stdout, stderr)
	case "cancel":
		attemptID, key, err := parseAgentCancel(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var agent runexecution.Agent
		if err := session.DoJSON(context.Background(), http.MethodGet, "agents/"+attemptID, nil, &agent); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		if !slices.Contains(agent.AllowedActions, runexecution.AgentActionCancel) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "AGENT_CANCEL_INVALID_TRANSITION", "agent cancellation is not allowed in the current state", false, ExitConflict)
		}
		if err := session.DoJSON(context.Background(), http.MethodPost, "agents/"+attemptID+"/cancel", nil, &agent,
			clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, agent.ResourceVersion))); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeAgent(agent, jsonOutput, stdout, stderr, command)
	case "permissions":
		return runAgentPermissions(args[1:], jsonOutput, stdout, stderr)
	default:
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar agent", "ARGUMENT_INVALID", fmt.Sprintf("unknown agent command %q", args[0]), false, ExitInvalidInput)
	}
}

func listAgents(command string, jsonOutput bool, stdout, stderr io.Writer) int {
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var list runexecution.AgentList
	if err := session.DoJSON(context.Background(), http.MethodGet, "agents", nil, &list); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	if jsonOutput {
		if err := writeJSON(stdout, agentMachineOutput{SchemaVersion: machineSchemaVersion, Result: list}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
		return int(ExitSuccess)
	}
	for _, agent := range list.Items {
		_, _ = fmt.Fprintf(stdout, "%s %s %s workspace=%s access=%s permissions=%s\n", agent.AttemptID, agent.Status, agent.Provider,
			agent.Execution.Workspace.ID, agent.Execution.Workspace.Access, strings.Join(agent.Execution.Permissions, ","))
	}
	return int(ExitSuccess)
}

func writeAgent(agent runexecution.Agent, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, agentMachineOutput{SchemaVersion: machineSchemaVersion, Result: agent}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
		return int(ExitSuccess)
	}
	_, _ = fmt.Fprintf(stdout, "%s %s\nProvider: %s\nRun: %s\nNode: %s\nWorkspace: %s (%s)\nPermissions: %s\nAllowed actions: %s\nElapsed: %s\n",
		agent.AttemptID, agent.Status, agent.Provider, agent.RunID, agent.NodeID, agent.Execution.Workspace.ID,
		agent.Execution.Workspace.Access, strings.Join(agent.Execution.Permissions, ", "), joinAgentActions(agent.AllowedActions), time.Duration(agent.ElapsedMilliseconds)*time.Millisecond)
	return int(ExitSuccess)
}

func joinAgentActions(actions []runexecution.AgentAction) string {
	values := make([]string, len(actions))
	for index := range actions {
		values[index] = string(actions[index])
	}
	return strings.Join(values, ", ")
}

func parseAgentLogs(args []string) (string, bool, error) {
	if len(args) < 1 || len(args) > 2 || !attemptIdentityPattern.MatchString(args[0]) {
		return "", false, errors.New("expected 'agent logs <attempt-id> [--follow]' with a canonical attempt_ ULID")
	}
	if len(args) == 2 && args[1] != "--follow" {
		return "", false, errors.New("agent logs accepts only --follow")
	}
	return args[0], len(args) == 2, nil
}

func parseAgentCancel(args []string) (string, string, error) {
	if len(args) != 1 && len(args) != 3 {
		return "", "", errors.New("expected 'agent cancel <attempt-id> [--idempotency-key <key>]' ")
	}
	if !attemptIdentityPattern.MatchString(args[0]) {
		return "", "", errors.New("agent cancel requires a canonical attempt_ ULID")
	}
	key := newIdempotencyKey()
	if len(args) == 3 {
		if args[1] != "--idempotency-key" || args[2] == "" {
			return "", "", errors.New("expected 'agent cancel <attempt-id> [--idempotency-key <key>]' ")
		}
		key = args[2]
	}
	return args[0], key, nil
}

func followAgentLog(command, attemptID string, follow, jsonOutput bool, stdout, stderr io.Writer) int {
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	offset := int64(0)
	emitted := false
	for {
		chunk, err := session.ReadLog(context.Background(), "agents/"+attemptID+"/logs", offset, 64<<10)
		if err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		if jsonOutput && (len(chunk.Content) != 0 || !follow) {
			if err := writeJSON(stdout, agentLogOutput{SchemaVersion: machineSchemaVersion, AttemptID: attemptID, Offset: chunk.Offset, NextOffset: chunk.NextOffset, Complete: chunk.Complete, Content: string(chunk.Content)}); err != nil {
				return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
			}
			emitted = true
		} else if len(chunk.Content) != 0 {
			_, _ = stdout.Write(chunk.Content)
			emitted = true
		}
		offset = chunk.NextOffset
		if !follow && chunk.Complete {
			return int(ExitSuccess)
		}
		if !chunk.Complete {
			continue
		}
		if chunk.Complete {
			var agent runexecution.Agent
			if err := session.DoJSON(context.Background(), http.MethodGet, "agents/"+attemptID, nil, &agent); err != nil {
				return writeClientError(stdout, stderr, jsonOutput, command, err)
			}
			if agent.Status.Terminal() {
				if jsonOutput && !emitted {
					if err := writeJSON(stdout, agentLogOutput{SchemaVersion: machineSchemaVersion, AttemptID: attemptID, Offset: chunk.Offset, NextOffset: chunk.NextOffset, Complete: true}); err != nil {
						return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
					}
				}
				return int(ExitSuccess)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
