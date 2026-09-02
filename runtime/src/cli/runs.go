package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

type runMachineOutput struct {
	SchemaVersion int `json:"schemaVersion"`
	Result        any `json:"result"`
}

func parseRunStart(args []string) (runexecution.CreateRequest, string, string, error) {
	if len(args) == 0 {
		return runexecution.CreateRequest{}, "", "", errors.New("expected run start <work-id> [--workflow <name>] [--version <version>] [--idempotency-key <key>]")
	}
	if args[0] == "--scenario" {
		if (len(args) != 2 && len(args) != 4) || args[1] == "" || (len(args) == 4 && (args[2] != "--idempotency-key" || args[3] == "")) {
			return runexecution.CreateRequest{}, "", "", errors.New("expected run start --scenario <fake-success|fake-restart> [--idempotency-key <key>]")
		}
		key := newIdempotencyKey()
		if len(args) == 4 {
			key = args[3]
		}
		return runexecution.CreateRequest{}, args[1], key, nil
	}
	if !workIdentityPattern.MatchString(args[0]) {
		return runexecution.CreateRequest{}, "", "", errors.New("run start requires a canonical work_ ULID")
	}
	request := runexecution.CreateRequest{WorkItemID: args[0], WorkflowID: runexecution.DefaultWorkflowID, WorkflowVersion: runexecution.DefaultWorkflowVersion}
	key := ""
	seenWorkflow, seenVersion := false, false
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return runexecution.CreateRequest{}, "", "", fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--workflow":
			if seenWorkflow {
				return runexecution.CreateRequest{}, "", "", errors.New("--workflow may be specified only once")
			}
			seenWorkflow, request.WorkflowID = true, value
		case "--version":
			if seenVersion {
				return runexecution.CreateRequest{}, "", "", errors.New("--version may be specified only once")
			}
			seenVersion, request.WorkflowVersion = true, value
		case "--idempotency-key":
			if key != "" {
				return runexecution.CreateRequest{}, "", "", errors.New("--idempotency-key may be specified only once")
			}
			key = value
		default:
			return runexecution.CreateRequest{}, "", "", fmt.Errorf("unknown run start option %q", args[index])
		}
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return request, "", key, nil
}

func parseRunList(args []string) (string, error) {
	limit, after := 50, ""
	seenLimit, seenAfter := false, false
	for index := 0; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return "", fmt.Errorf("%s requires a value", args[index])
		}
		switch args[index] {
		case "--limit":
			if seenLimit {
				return "", errors.New("--limit may be specified only once")
			}
			value, err := strconv.Atoi(args[index+1])
			if err != nil || value < 1 || value > 200 {
				return "", errors.New("--limit must be between 1 and 200")
			}
			seenLimit, limit = true, value
		case "--after":
			if seenAfter || !runIdentityPattern.MatchString(args[index+1]) {
				return "", errors.New("--after requires one canonical run_ cursor")
			}
			seenAfter, after = true, args[index+1]
		default:
			return "", fmt.Errorf("unknown run list option %q", args[index])
		}
	}
	values := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if after != "" {
		values.Set("after", after)
	}
	return "runs?" + values.Encode(), nil
}

func writeRunProjectionResult(result statestore.RunProjection, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, runMachineOutput{SchemaVersion: machineSchemaVersion, Result: result}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "Started %s for %s: %s.\n", result.RunID, result.WorkItemID, result.Status)
	}
	return int(ExitSuccess)
}

func writeRunPage(page runexecution.Page, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, runMachineOutput{SchemaVersion: machineSchemaVersion, Result: page}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
		return int(ExitSuccess)
	}
	var output strings.Builder
	for _, run := range page.Items {
		_, _ = fmt.Fprintf(&output, "%s %s %s %s@%s\n", run.RunID, run.Status, run.WorkItemID, run.WorkflowID, run.WorkflowVersion)
	}
	if output.Len() != 0 {
		_, _ = fmt.Fprint(stdout, strings.TrimSuffix(output.String(), "\n")+"\n")
	}
	return int(ExitSuccess)
}
