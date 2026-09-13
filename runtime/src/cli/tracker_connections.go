package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"darkstar/src/ports/trackerconnection"
)

func runTracker(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar tracker"
	if len(args) < 2 {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected tracker credential store <ref> --stdin or tracker connection <add-linear|add-github-token|add-github-cli|list|show|health|destinations>"))
	}
	if args[0] == "credential" {
		return runTrackerCredential(args[1:], jsonOutput, stdout, stderr)
	}
	if args[0] != "connection" {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unknown tracker command"))
	}
	command += " connection " + args[1]
	resource := "tracker/connections"
	method := http.MethodGet
	var input any
	flags := map[string]string{}
	if args[1] != "list" {
		if len(args) < 3 || args[2] == "" || strings.ContainsAny(args[2], "/\\") {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("a connection ID is required"))
		}
		for index := 3; index < len(args); index += 2 {
			if index+1 >= len(args) || !strings.HasPrefix(args[index], "--") {
				return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("each flag requires a value"))
			}
			if _, exists := flags[args[index]]; exists {
				return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("repeated tracker flag"))
			}
			flags[args[index]] = args[index+1]
		}
		if flags["--revision"] == "" {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--revision is required; connection revisions are immutable"))
		}
	}
	allowed := map[string]bool{"--revision": true}
	switch args[1] {
	case "list":
		if len(args) != 2 {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("connection list accepts no arguments"))
		}
	case "add-linear":
		method = http.MethodPost
		resource += "/linear"
		allowed["--credential-ref"] = true
		allowed["--authentication"] = true
		input = trackerconnection.LinearSetupRequest{SchemaVersion: 1, ConnectionID: args[2], Revision: flags["--revision"], CredentialRef: flags["--credential-ref"], Authentication: flags["--authentication"]}
	case "add-github-token":
		method = http.MethodPost
		resource += "/github-token"
		allowed["--host"] = true
		allowed["--credential-ref"] = true
		input = trackerconnection.GitHubTokenSetupRequest{SchemaVersion: 1, ConnectionID: args[2], Revision: flags["--revision"], Host: flags["--host"], CredentialRef: flags["--credential-ref"]}
	case "add-github-cli":
		method = http.MethodPost
		resource += "/github-cli"
		allowed["--host"] = true
		allowed["--login"] = true
		input = trackerconnection.GitHubCLISetupRequest{SchemaVersion: 1, ConnectionID: args[2], Revision: flags["--revision"], Host: flags["--host"], Login: flags["--login"]}
	case "show", "health", "destinations":
		resource += "/" + url.PathEscape(args[2]) + "/revisions/" + url.PathEscape(flags["--revision"])
		if args[1] != "show" {
			resource += "/" + args[1]
		}
		if args[1] == "destinations" {
			allowed["--cursor"] = true
			allowed["--page-size"] = true
			query := url.Values{}
			if value := flags["--cursor"]; value != "" {
				query.Set("cursor", value)
			}
			if value := flags["--page-size"]; value != "" {
				query.Set("pageSize", value)
			}
			if len(query) != 0 {
				resource += "?" + query.Encode()
			}
		}
	default:
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unknown tracker connection command"))
	}
	for flag := range flags {
		if !allowed[flag] {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unsupported tracker connection option"))
		}
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var result json.RawMessage
	if err := session.DoJSON(context.Background(), method, resource, input, &result); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	var human bytes.Buffer
	if err := json.Indent(&human, result, "", "  "); err != nil {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("tracker response could not be formatted"))
	}
	return writeWorkResult(result, human.String(), jsonOutput, stdout, stderr, command)
}

func runTrackerCredential(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar tracker credential store"
	if len(args) != 3 || args[0] != "store" || args[2] != "--stdin" || args[1] == "" || strings.ContainsAny(args[1], "/\\") {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected tracker credential store <ref> --stdin; secret values must come from stdin"))
	}
	content, err := io.ReadAll(io.LimitReader(os.Stdin, 8195))
	if err != nil || len(content) > 8194 {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("credential input is unavailable or exceeds 8 KiB"))
	}
	secret := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	request := struct {
		SchemaVersion int    `json:"schemaVersion"`
		Secret        string `json:"secret"`
	}{SchemaVersion: 1, Secret: secret}
	var response struct {
		SchemaVersion int  `json:"schemaVersion"`
		Stored        bool `json:"stored"`
	}
	if err := session.DoJSON(context.Background(), http.MethodPost, "tracker/credentials/"+url.PathEscape(args[1]), request, &response); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeWorkResult(response, "Protected tracker credential stored.", jsonOutput, stdout, stderr, command)
}
