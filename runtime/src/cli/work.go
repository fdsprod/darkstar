package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

var (
	projectIdentityPattern = regexp.MustCompile(`^project_[0-9A-HJKMNP-TV-Z]{26}$`)
	workIdentityPattern    = regexp.MustCompile(`^work_[0-9A-HJKMNP-TV-Z]{26}$`)
)

type workMachineOutput struct {
	SchemaVersion int `json:"schemaVersion"`
	Result        any `json:"result"`
}

func runProject(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return workArgumentError(stdout, stderr, jsonOutput, "darkstar project", errors.New("a project command is required (add, register, list, show)"))
	}
	command := "darkstar project " + args[0]
	switch args[0] {
	case "add", "register":
		registration, key, err := parseProjectRegistration(args[1:])
		if err != nil {
			return workArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result statestore.ProjectProjection
		if err := session.DoJSON(context.Background(), http.MethodPost, "projects", registration, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkResult(result, fmt.Sprintf("Registered %s (%s).", result.Name, result.ProjectID), jsonOutput, stdout, stderr, command)
	case "list":
		if len(args) != 1 {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected project list"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result []statestore.ProjectProjection
		if err := session.DoJSON(context.Background(), http.MethodGet, "projects", nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		for _, project := range result {
			_, _ = fmt.Fprintf(&human, "%s %s %s\n", project.ProjectID, project.Status, project.Name)
		}
		return writeWorkResult(result, strings.TrimSuffix(human.String(), "\n"), jsonOutput, stdout, stderr, command)
	case "show":
		if len(args) != 2 || !projectIdentityPattern.MatchString(args[1]) {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected project show <project-id> with a canonical project_ ULID"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result workmanagement.ProjectView
		if err := session.DoJSON(context.Background(), http.MethodGet, "projects/"+args[1], nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		human := fmt.Sprintf("%s %s: %s (%d work items).", result.Project.ProjectID, result.Project.Status, result.Project.Name, len(result.WorkItems))
		return writeWorkResult(result, human, jsonOutput, stdout, stderr, command)
	default:
		return workArgumentError(stdout, stderr, jsonOutput, "darkstar project", fmt.Errorf("unknown project command %q", args[0]))
	}
}

func runWork(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return workArgumentError(stdout, stderr, jsonOutput, "darkstar work", errors.New("a work command is required (create, import, list, show)"))
	}
	command := "darkstar work " + args[0]
	switch args[0] {
	case "create", "import":
		input, key, err := parseWorkMutation(args[0], args[1:])
		if err != nil {
			return workArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		projectID := input.projectID
		if projectID == "" {
			projectID, err = selectOnlyActiveProject(context.Background(), session)
			if err != nil {
				return workArgumentError(stdout, stderr, jsonOutput, command, err)
			}
		}
		var result statestore.WorkItemProjection
		resource := "work-items"
		var request any = workmanagement.CreateWorkRequest{ProjectID: projectID, Title: input.value, Priority: input.priority}
		if args[0] == "import" {
			resource += "/import"
			request = workmanagement.ImportWorkRequest{ProjectID: projectID, SourceReference: input.value, Title: input.title, Priority: input.priority}
		}
		if err := session.DoJSON(context.Background(), http.MethodPost, resource, request, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkResult(result, fmt.Sprintf("Created %s: %s.", result.WorkItemID, result.Title), jsonOutput, stdout, stderr, command)
	case "list":
		projectID, err := parseWorkList(args[1:])
		if err != nil {
			return workArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		resource := "work-items"
		if projectID != "" {
			resource += "?projectId=" + url.QueryEscape(projectID)
		}
		var result []statestore.WorkItemProjection
		if err := session.DoJSON(context.Background(), http.MethodGet, resource, nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		for _, work := range result {
			_, _ = fmt.Fprintf(&human, "%s %s %d %s\n", work.WorkItemID, work.Status, work.Priority, work.Title)
		}
		return writeWorkResult(result, strings.TrimSuffix(human.String(), "\n"), jsonOutput, stdout, stderr, command)
	case "show":
		if len(args) != 2 || !workIdentityPattern.MatchString(args[1]) {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected work show <work-id> with a canonical work_ ULID"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result workmanagement.WorkView
		if err := session.DoJSON(context.Background(), http.MethodGet, "work-items/"+args[1], nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		human := fmt.Sprintf("%s %s: %s (%d runs, %d stories).", result.Work.WorkItemID, result.Work.Status, result.Work.Title, len(result.Runs), len(result.Stories))
		return writeWorkResult(result, human, jsonOutput, stdout, stderr, command)
	default:
		return workArgumentError(stdout, stderr, jsonOutput, "darkstar work", fmt.Errorf("unknown work command %q", args[0]))
	}
}

func parseProjectRegistration(args []string) (workmanagement.ProjectRegistration, string, error) {
	pathValue, name, key := "", "", ""
	for index := 0; index < len(args); {
		switch args[index] {
		case "--name", "--idempotency-key":
			if index+1 >= len(args) || args[index+1] == "" {
				return workmanagement.ProjectRegistration{}, "", fmt.Errorf("%s requires a value", args[index])
			}
			if args[index] == "--name" {
				if name != "" {
					return workmanagement.ProjectRegistration{}, "", errors.New("--name may be specified only once")
				}
				name = args[index+1]
			} else {
				if key != "" {
					return workmanagement.ProjectRegistration{}, "", errors.New("--idempotency-key may be specified only once")
				}
				key = args[index+1]
			}
			index += 2
		default:
			if strings.HasPrefix(args[index], "--") || pathValue != "" {
				return workmanagement.ProjectRegistration{}, "", fmt.Errorf("unexpected project argument %q", args[index])
			}
			pathValue = args[index]
			index++
		}
	}
	if pathValue == "" {
		var err error
		pathValue, err = os.Getwd()
		if err != nil {
			return workmanagement.ProjectRegistration{}, "", fmt.Errorf("resolve current project: %w", err)
		}
	}
	absolute, err := filepath.Abs(pathValue)
	if err != nil {
		return workmanagement.ProjectRegistration{}, "", fmt.Errorf("resolve project path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return workmanagement.ProjectRegistration{}, "", fmt.Errorf("project path must be an existing directory: %s", absolute)
	}
	if name == "" {
		name = filepath.Base(filepath.Clean(absolute))
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return workmanagement.ProjectRegistration{Name: name, Source: filepath.Clean(absolute)}, key, nil
}

type parsedWorkMutation struct {
	value, projectID, title string
	priority                int
}

func parseWorkMutation(kind string, args []string) (parsedWorkMutation, string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "--") {
		return parsedWorkMutation{}, "", fmt.Errorf("expected work %s <%s>", kind, map[string]string{"create": "description", "import": "source-ref"}[kind])
	}
	result, key := parsedWorkMutation{value: args[0]}, ""
	seenPriority := false
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return parsedWorkMutation{}, "", fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--project":
			if result.projectID != "" || !projectIdentityPattern.MatchString(value) {
				return parsedWorkMutation{}, "", errors.New("--project requires one canonical project_ ULID")
			}
			result.projectID = value
		case "--title":
			if kind != "import" || result.title != "" {
				return parsedWorkMutation{}, "", errors.New("--title is supported once for work import")
			}
			result.title = value
		case "--priority":
			if seenPriority {
				return parsedWorkMutation{}, "", errors.New("--priority may be specified only once")
			}
			priority, err := strconv.Atoi(value)
			if err != nil || priority < 0 {
				return parsedWorkMutation{}, "", errors.New("--priority requires a non-negative integer")
			}
			seenPriority, result.priority = true, priority
		case "--idempotency-key":
			if key != "" {
				return parsedWorkMutation{}, "", errors.New("--idempotency-key may be specified only once")
			}
			key = value
		default:
			return parsedWorkMutation{}, "", fmt.Errorf("unknown work option %q", args[index])
		}
	}
	if key == "" {
		key = newIdempotencyKey()
	}
	return result, key, nil
}

func parseWorkList(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) != 2 || args[0] != "--project" || !projectIdentityPattern.MatchString(args[1]) {
		return "", errors.New("expected work list [--project <project-id>]")
	}
	return args[1], nil
}

func selectOnlyActiveProject(ctx context.Context, session *clientapi.Session) (string, error) {
	var projects []statestore.ProjectProjection
	if err := session.DoJSON(ctx, http.MethodGet, "projects", nil, &projects); err != nil {
		return "", err
	}
	active := make([]statestore.ProjectProjection, 0, len(projects))
	for _, project := range projects {
		if project.Status == statestore.ProjectActive {
			active = append(active, project)
		}
	}
	if len(active) == 0 {
		return "", errors.New("no active project is registered; run 'darkstar project add' or pass --project")
	}
	if len(active) > 1 {
		return "", errors.New("more than one active project is registered; pass --project")
	}
	return active[0].ProjectID, nil
}

func writeWorkResult(result any, human string, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, workMachineOutput{SchemaVersion: machineSchemaVersion, Result: result}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else if human != "" {
		_, _ = fmt.Fprintln(stdout, human)
	}
	return int(ExitSuccess)
}

func workArgumentError(stdout, stderr io.Writer, jsonOutput bool, command string, err error) int {
	return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
}
