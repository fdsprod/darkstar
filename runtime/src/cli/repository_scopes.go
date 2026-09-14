package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/repositoryscope"
)

var scopeIdentityPattern = regexp.MustCompile(`^scope_[0-9A-HJKMNP-TV-Z]{26}$`)

func runRepositoryScope(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	const command = "darkstar project scope"
	method, resource, input, key, err := parseRepositoryScopeCommand(args)
	if err != nil {
		return workArgumentError(stdout, stderr, jsonOutput, command, err)
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var options []clientapi.RequestOption
	if key != "" {
		options = append(options, clientapi.WithHeader("Idempotency-Key", key))
	}
	var result repositoryscope.View
	if err := session.DoJSON(context.Background(), method, resource, input, &result, options...); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	human := fmt.Sprintf("%s: %s, %s (%d repositories). Working-tree edits are excluded.", result.Scope.ScopeID, result.Scope.Mode, result.Preparation.Status, len(result.Scope.Repositories))
	if result.Preparation.Reason != "" {
		human += "\n" + result.Preparation.Reason
	}
	return writeWorkResult(result, human, jsonOutput, stdout, stderr, command)
}

func parseRepositoryScopeCommand(args []string) (string, string, any, string, error) {
	invalid := func(message string) (string, string, any, string, error) {
		return "", "", nil, "", errors.New(message)
	}
	if len(args) == 2 && args[0] == "show" && scopeIdentityPattern.MatchString(args[1]) {
		return http.MethodGet, "investigation-scopes/" + args[1], nil, "", nil
	}
	if len(args) < 4 || args[0] != "prepare" || !projectIdentityPattern.MatchString(args[1]) {
		return invalid("expected project scope prepare <project-id> --repositories-file <json-file> [--idempotency-key <key>], or project scope show <scope-id>")
	}
	flags := map[string]string{}
	for index := 2; index < len(args); index += 2 {
		flag := args[index]
		if index+1 == len(args) || (flag != "--repositories-file" && flag != "--idempotency-key") || flags[flag] != "" {
			return invalid("scope flags require one value each and cannot be repeated")
		}
		flags[flag] = args[index+1]
	}
	if strings.TrimSpace(flags["--repositories-file"]) == "" {
		return invalid("--repositories-file is required; use an explicit [] for a scope without repositories")
	}
	data, err := os.ReadFile(flags["--repositories-file"])
	if err != nil {
		return invalid("read repository selections: " + err.Error())
	}
	if len(data) > 64<<10 {
		return invalid("repository selection file exceeds 64 KiB")
	}
	var repositories []repositoryscope.RepositorySelection
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&repositories); err != nil || repositories == nil || decoder.Decode(new(any)) != io.EOF {
		return invalid("repository selection file must be one JSON array of repositoryId/ref objects; use [] for no repositories")
	}
	key := flags["--idempotency-key"]
	if key == "" {
		key = newIdempotencyKey()
	}
	if strings.TrimSpace(key) != key || len(key) < 8 || len(key) > 128 {
		return invalid("idempotency key must be 8 to 128 bytes without surrounding whitespace")
	}
	return http.MethodPost, "investigation-scopes", repositoryscope.PrepareRequest{ProjectID: args[1], Repositories: repositories}, key, nil
}
