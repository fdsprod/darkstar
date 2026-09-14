package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	clientapi "darkstar/src/api/client"
)

func runTrackerMapping(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar tracker mapping"
	if len(args) < 2 || args[1] == "" || strings.ContainsAny(args[1], "/\\") {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected tracker mapping <show|discover|save|activate|preview|board|transition> <project-id> [--file request.json] [--observation id]"))
	}
	flags := map[string]string{}
	for index := 2; index < len(args); index += 2 {
		if index+1 >= len(args) || flags[args[index]] != "" || (args[index] != "--file" && args[index] != "--observation" && args[index] != "--key") {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unsupported, repeated or missing mapping option value"))
		}
		flags[args[index]] = args[index+1]
	}
	resource := "projects/" + url.PathEscape(args[1]) + "/tracker-mapping"
	method := http.MethodGet
	var input json.RawMessage
	switch args[0] {
	case "show":
	case "discover":
		resource += "/discovery"
		if flags["--observation"] != "" {
			resource += "?observationId=" + url.QueryEscape(flags["--observation"])
		}
	case "board":
		resource = "projects/" + url.PathEscape(args[1]) + "/tracker-board"
	case "save", "activate", "preview", "transition":
		method = http.MethodPost
		if args[0] == "transition" {
			resource = "projects/" + url.PathEscape(args[1]) + "/tracker-board/transition"
		} else if args[0] != "save" {
			resource += "/" + args[0]
		}
		if err := readBacklogJSON(flags["--file"], &input); err != nil {
			return workArgumentError(stdout, stderr, jsonOutput, command, err)
		}
	default:
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unknown tracker mapping operation"))
	}
	if method == http.MethodGet && (flags["--file"] != "" || flags["--key"] != "") || args[0] != "discover" && flags["--observation"] != "" {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("file/key are command options; observation is a discovery option"))
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	key := flags["--key"]
	if key == "" {
		key = newIdempotencyKey()
	}
	var result json.RawMessage
	var body any
	if method == http.MethodPost {
		body = input
	}
	if err := session.DoJSON(context.Background(), method, resource, body, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	var human bytes.Buffer
	if err := json.Indent(&human, result, "", "  "); err != nil {
		return workArgumentError(stdout, stderr, jsonOutput, command, err)
	}
	return writeWorkResult(result, human.String(), jsonOutput, stdout, stderr, command)
}
