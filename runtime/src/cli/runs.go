package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"darkstar/src/core/preparation"
	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

var runNodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type runMachineOutput struct {
	SchemaVersion int `json:"schemaVersion"`
	Result        any `json:"result"`
}

func parseRunStart(args []string) (runexecution.CreateRequest, string, string, error) {
	if len(args) == 0 {
		return runexecution.CreateRequest{}, "", "", errors.New("expected run start <work-id> [--workflow <name>] [--version <version>] [--profile <profile>] [--idempotency-key <key>]")
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
	request, key, err := parseWorkRun(args, "start")
	return request, "", key, err
}

func parseRunPrepare(args []string) (runexecution.CreateRequest, string, error) {
	return parseWorkRun(args, "prepare")
}

func parseWorkRun(args []string, action string) (runexecution.CreateRequest, string, error) {
	if len(args) == 0 {
		return runexecution.CreateRequest{}, "", fmt.Errorf("expected run %s <work-id> [--workflow <name>] [--version <version>] [--profile <profile>] [--idempotency-key <key>]", action)
	}
	if !workIdentityPattern.MatchString(args[0]) {
		return runexecution.CreateRequest{}, "", fmt.Errorf("run %s requires a canonical work_ ULID", action)
	}
	request := runexecution.CreateRequest{WorkItemID: args[0]}
	key := ""
	seenWorkflow, seenVersion, seenProfile := false, false, false
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return runexecution.CreateRequest{}, "", fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--workflow":
			if seenWorkflow {
				return runexecution.CreateRequest{}, "", errors.New("--workflow may be specified only once")
			}
			seenWorkflow, request.WorkflowID = true, value
		case "--version":
			if seenVersion {
				return runexecution.CreateRequest{}, "", errors.New("--version may be specified only once")
			}
			seenVersion, request.WorkflowVersion = true, value
		case "--profile":
			if seenProfile || !runNodePattern.MatchString(value) {
				return runexecution.CreateRequest{}, "", errors.New("--profile requires one workflow profile identifier")
			}
			seenProfile, request.Profile = true, value
		case "--answers-json", "--inputs-json":
			if request.Preparation == nil {
				request.Preparation = &runexecution.PreparationInput{}
			}
			if args[index] == "--answers-json" {
				if request.Preparation.Answers != nil || json.Unmarshal([]byte(value), &request.Preparation.Answers) != nil || request.Preparation.Answers == nil {
					return runexecution.CreateRequest{}, "", errors.New("--answers-json requires one JSON object of question IDs to string answers")
				}
			} else {
				if request.Preparation.RunInputs != nil || json.Unmarshal([]byte(value), &request.Preparation.RunInputs) != nil || request.Preparation.RunInputs == nil {
					return runexecution.CreateRequest{}, "", errors.New("--inputs-json requires one JSON object of workflow input names to values")
				}
				for name := range request.Preparation.RunInputs {
					if !runNodePattern.MatchString(string(name)) {
						return runexecution.CreateRequest{}, "", errors.New("--inputs-json keys must be workflow input identifiers")
					}
				}
			}
		case "--idempotency-key":
			if key != "" {
				return runexecution.CreateRequest{}, "", errors.New("--idempotency-key may be specified only once")
			}
			key = value
		default:
			return runexecution.CreateRequest{}, "", fmt.Errorf("unknown run %s option %q", action, args[index])
		}
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return request, key, nil
}

func parseRunLaunch(args []string) (string, uint64, string, string, error) {
	if len(args) == 0 || !runIdentityPattern.MatchString(args[0]) {
		return "", 0, "", "", errors.New("run launch requires a canonical run_ ULID")
	}
	var revision uint64
	key, digest := "", ""
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return "", 0, "", "", fmt.Errorf("%s requires a value", args[index])
		}
		switch args[index] {
		case "--if-match":
			if revision != 0 {
				return "", 0, "", "", errors.New("--if-match may be specified only once")
			}
			value, err := strconv.ParseUint(args[index+1], 10, 64)
			if err != nil || value == 0 {
				return "", 0, "", "", errors.New("--if-match requires a positive run resource version")
			}
			revision = value
		case "--confirm-assessment":
			if digest != "" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(args[index+1]) {
				return "", 0, "", "", errors.New("--confirm-assessment requires one 64-character lowercase SHA-256 digest")
			}
			digest = args[index+1]
		case "--idempotency-key":
			if key != "" {
				return "", 0, "", "", errors.New("--idempotency-key may be specified only once")
			}
			key = args[index+1]
		default:
			return "", 0, "", "", fmt.Errorf("unknown run launch option %q", args[index])
		}
	}
	if revision == 0 {
		return "", 0, "", "", errors.New("run launch requires --if-match <version>")
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return args[0], revision, key, digest, nil
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

func parseSimpleRunControl(args []string, action string) (string, string, error) {
	if len(args) != 1 && len(args) != 3 {
		return "", "", fmt.Errorf("expected run %s <run-id> [--idempotency-key <key>]", action)
	}
	if !runIdentityPattern.MatchString(args[0]) {
		return "", "", fmt.Errorf("run %s requires a canonical run_ ULID", action)
	}
	key := newIdempotencyKey()
	if len(args) == 3 {
		if args[1] != "--idempotency-key" || args[2] == "" {
			return "", "", fmt.Errorf("expected run %s <run-id> [--idempotency-key <key>]", action)
		}
		key = args[2]
	}
	return args[0], key, nil
}

func parseRunRetry(args []string) (string, string, string, error) {
	if len(args) == 0 || !runIdentityPattern.MatchString(args[0]) {
		return "", "", "", errors.New("run retry requires a canonical run_ ULID")
	}
	nodeID, key := "", ""
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return "", "", "", fmt.Errorf("%s requires a value", args[index])
		}
		switch args[index] {
		case "--node":
			if nodeID != "" || !runNodePattern.MatchString(args[index+1]) {
				return "", "", "", errors.New("--node requires one workflow node identifier")
			}
			nodeID = args[index+1]
		case "--idempotency-key":
			if key != "" {
				return "", "", "", errors.New("--idempotency-key may be specified only once")
			}
			key = args[index+1]
		default:
			return "", "", "", fmt.Errorf("unknown run retry option %q", args[index])
		}
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return args[0], nodeID, key, nil
}

func parseRunContinue(args []string) (string, string, string, error) {
	if len(args) == 0 || !runIdentityPattern.MatchString(args[0]) {
		return "", "", "", errors.New("run continue requires a canonical run_ ULID")
	}
	until, key := "", ""
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return "", "", "", fmt.Errorf("%s requires a value", args[index])
		}
		switch args[index] {
		case "--until":
			if until != "" || !runNodePattern.MatchString(args[index+1]) {
				return "", "", "", errors.New("--until requires one workflow node identifier")
			}
			until = args[index+1]
		case "--idempotency-key":
			if key != "" {
				return "", "", "", errors.New("--idempotency-key may be specified only once")
			}
			key = args[index+1]
		default:
			return "", "", "", fmt.Errorf("unknown run continue option %q", args[index])
		}
	}
	if until == "" {
		return "", "", "", errors.New("run continue requires --until <node>")
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return args[0], until, key, nil
}

func writeRunProjectionResult(result statestore.RunProjection, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	return writeRunProjectionActionResult(result, "Started", jsonOutput, stdout, stderr, command)
}

func writeRunProjectionActionResult(result statestore.RunProjection, action string, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, runMachineOutput{SchemaVersion: machineSchemaVersion, Result: result}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "%s %s for %s: %s.\n", action, result.RunID, result.WorkItemID, result.Status)
		var route struct {
			Assessment *preparation.Assessment `json:"assessment"`
		}
		if json.Unmarshal([]byte(result.RouteSnapshot), &route) == nil && route.Assessment != nil {
			assessment := route.Assessment
			_, _ = fmt.Fprintf(stdout, "Route assessment: %s\n%s\nDigest: %s\n", assessment.Readiness(), assessment.Rationale, assessment.Digest)
			for _, question := range assessment.Questions {
				_, _ = fmt.Fprintf(stdout, "Input %s: %s\n", question.ID, question.Prompt)
			}
			for _, reason := range assessment.ConfirmationReasons {
				_, _ = fmt.Fprintf(stdout, "Confirmation: %s\n", reason)
			}
		}

	}
	return int(ExitSuccess)
}

func writeRunControlResult(result statestore.RunProjection, action string, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, runMachineOutput{SchemaVersion: machineSchemaVersion, Result: result}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "%s %s: %s.\n", action, result.RunID, result.Status)
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
