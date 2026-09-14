package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	localapi "darkstar/src/api"
	clientapi "darkstar/src/api/client"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

func runProjectRepositories(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar project " + args[0]
	method, resource, input, options, err := projectRepositoryCommand(args)
	if err != nil {
		return workArgumentError(stdout, stderr, jsonOutput, command, err)
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	if args[0] == "list-v2" || args[0] == "discover" {
		var result []workmanagement.ProjectRepositoriesView
		if err := session.DoJSON(context.Background(), method, resource, input, &result, options...); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		for _, value := range result {
			_, _ = fmt.Fprintf(&human, "%s %s (%d active repositories)\n", value.Project.ProjectID, value.Project.Name, activeRepositoryCount(value.Repositories))
		}
		return writeWorkResult(result, strings.TrimSuffix(human.String(), "\n"), jsonOutput, stdout, stderr, command)
	}
	var result workmanagement.ProjectRepositoriesView
	if err := session.DoJSON(context.Background(), method, resource, input, &result, options...); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	human := fmt.Sprintf("%s: %s (%d active repositories; project revision %d).", result.Project.ProjectID, result.Project.Name, activeRepositoryCount(result.Repositories), result.Project.ResourceVersion)
	if result.Migration.State == "legacy_unresolved" {
		human += "\nRepository migration requires attention: " + result.Migration.Reason
	}
	for _, value := range result.Repositories {
		human += fmt.Sprintf("\n%s %s %s %s (membership revision %d)", value.Repository.RepositoryID, value.Membership.Status, value.Membership.Role, value.Membership.Label, value.Membership.Revision)
	}
	return writeWorkResult(result, human, jsonOutput, stdout, stderr, command)
}

func activeRepositoryCount(values []statestore.ProjectRepository) int {
	count := 0
	for _, value := range values {
		if value.Membership.Status == statestore.MembershipActive {
			count++
		}
	}
	return count
}

func projectRepositoryCommand(args []string) (string, string, any, []clientapi.RequestOption, error) {
	method, resource := http.MethodGet, "projects-v2"
	var input any
	var options []clientapi.RequestOption
	invalid := func(message string) (string, string, any, []clientapi.RequestOption, error) {
		return "", "", nil, nil, errors.New(message)
	}
	if len(args) == 0 {
		return invalid("a project command is required")
	}
	action, arguments := args[0], args[1:]
	if action == "repository" {
		if len(arguments) < 1 {
			return invalid("expected project repository <list|add|update|remove>")
		}
		action = "repository-" + arguments[0]
		arguments = arguments[1:]
	}
	positionals := []string{}
	flags := map[string]string{}
	for index := 0; index < len(arguments); index++ {
		value := arguments[index]
		if !strings.HasPrefix(value, "--") {
			positionals = append(positionals, value)
			continue
		}
		if index+1 == len(arguments) || strings.HasPrefix(arguments[index+1], "--") {
			return invalid(value + " requires a value")
		}
		if _, exists := flags[value]; exists {
			return invalid(value + " may be specified only once")
		}
		index++
		flags[value] = arguments[index]
	}
	allowed := map[string]bool{}
	switch action {
	case "list-v2":
		if len(positionals) != 0 {
			return invalid("expected project list-v2")
		}
	case "discover":
		if len(positionals) != 1 {
			return invalid("expected project discover <repository-path>")
		}
		absolute, err := filepath.Abs(positionals[0])
		if err != nil {
			return invalid(err.Error())
		}
		resource += "/discover?path=" + url.QueryEscape(absolute)
	case "create":
		if len(positionals) != 1 || strings.TrimSpace(positionals[0]) == "" {
			return invalid("expected project create <name> [--defaults-file <json-file>]")
		}
		allowed["--defaults-file"] = true
		var defaults statestore.RepositorySettings
		if err := readRepositorySettings(flags["--defaults-file"], &defaults); err != nil {
			return invalid(err.Error())
		}
		method = http.MethodPost
		input = workmanagement.CreateProjectRequest{Name: positionals[0], Defaults: defaults}
	case "show-v2", "repository-list", "defaults", "repository-add", "repository-update", "repository-remove":
		if len(positionals) < 1 || !projectIdentityPattern.MatchString(positionals[0]) {
			return invalid("a canonical project ID is required")
		}
		resource += "/" + positionals[0]
		if action == "show-v2" || action == "repository-list" {
			if len(positionals) != 1 {
				return invalid("this command accepts one project ID")
			}
			break
		}
		allowed["--revision"] = true
		revision, err := strconv.ParseUint(flags["--revision"], 10, 64)
		if err != nil || revision == 0 {
			return invalid("--revision requires the current positive project resource version")
		}
		options = append(options, clientapi.WithHeader("If-Match", fmt.Sprintf("\"%d\"", revision)))
		if action == "defaults" {
			if len(positionals) != 1 || flags["--defaults-file"] == "" {
				return invalid("expected project defaults <project-id> --defaults-file <json-file> --revision <n>")
			}
			allowed["--defaults-file"] = true
			var defaults statestore.RepositorySettings
			if err := readRepositorySettings(flags["--defaults-file"], &defaults); err != nil {
				return invalid(err.Error())
			}
			method, resource = http.MethodPut, resource+"/defaults"
			input = struct {
				Defaults statestore.RepositorySettings `json:"defaults"`
			}{Defaults: defaults}
			break
		}
		if len(positionals) != 2 {
			return invalid("expected a project ID followed by a repository path for add, or a repository ID for update/remove")
		}
		allowed["--membership-revision"] = true
		membershipRevision := uint64(0)
		if value, present := flags["--membership-revision"]; present {
			membershipRevision, err = strconv.ParseUint(value, 10, 64)
			if err != nil {
				return invalid("--membership-revision requires a nonnegative integer")
			}
		}
		if action != "repository-add" && membershipRevision == 0 {
			return invalid("--membership-revision requires the current positive membership revision")
		}
		resource += "/repositories"
		if action != "repository-add" {
			resource += "/" + url.PathEscape(positionals[1])
		}
		if action == "repository-remove" {
			method = http.MethodDelete
			input = localapi.RepositoryMembershipRemovalInput{ExpectedMembershipRevision: membershipRevision}
			break
		}
		allowed["--role"], allowed["--label"], allowed["--settings-file"] = true, true, true
		if strings.TrimSpace(flags["--label"]) == "" {
			return invalid("--label requires a project-local repository name")
		}
		if flags["--role"] != "read_only" && flags["--role"] != "implementation" {
			return invalid("--role must be read_only or implementation")
		}
		var settings statestore.RepositorySettings
		if err := readRepositorySettings(flags["--settings-file"], &settings); err != nil {
			return invalid(err.Error())
		}
		membership := localapi.RepositoryMembershipInput{Label: flags["--label"], Role: statestore.RepositoryRole(flags["--role"]), Settings: settings, ExpectedMembershipRevision: membershipRevision}
		method = http.MethodPut
		if action == "repository-add" {
			method = http.MethodPost
			membership.RepositoryPath, err = filepath.Abs(positionals[1])
			if err != nil {
				return invalid(err.Error())
			}
		}
		input = membership
	default:
		return invalid("unknown project repository command")
	}
	if method != http.MethodGet {
		allowed["--idempotency-key"] = true
		key := flags["--idempotency-key"]
		if key == "" {
			key = newIdempotencyKey()
		}
		options = append(options, clientapi.WithHeader("Idempotency-Key", key))
	}
	for flag := range flags {
		if !allowed[flag] {
			return invalid("unexpected flag " + flag)
		}
	}
	return method, resource, input, options, nil
}

func readRepositorySettings(filename string, result *statestore.RepositorySettings) error {
	if filename == "" {
		return nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read repository settings: %w", err)
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("repository settings must contain exactly one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("decode repository settings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("repository settings must contain exactly one JSON object")
	}
	return nil
}
