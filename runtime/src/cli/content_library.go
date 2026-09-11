package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"darkstar/src/core/contentlibrary"
)

func runContent(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar content"
	method := http.MethodGet
	endpoint := "content-library"
	var input any
	if len(args) == 0 {
		return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected list, show, create, update, publish, duplicate, archive, restore, or preview"))
	}
	command += " " + args[0]
	fail := func(message string) int {
		return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New(message))
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fail("expected content list")
		}
	case "show":
		if len(args) != 2 {
			return fail("expected content show <id>")
		}
		endpoint += "/" + url.PathEscape(args[1])
	case "create", "preview", "update":
		want := 2
		if args[0] == "update" {
			want = 3
		}
		if len(args) != want {
			return fail("expected content create|preview <request.json> or content update <id> <request.json>")
		}
		raw, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		if !json.Valid(raw) || len(raw) > 1<<20 {
			return fail("request file must contain valid JSON no larger than 1 MiB")
		}
		input = json.RawMessage(raw)
		method = http.MethodPost
		switch args[0] {
		case "preview":
			endpoint += "/preview"
		case "update":
			endpoint += "/" + url.PathEscape(args[1]) + "/draft"
			method = http.MethodPut
		}
	case "publish":
		if len(args) != 5 || args[3] != "--revision" {
			return fail("expected content publish <id> <version> --revision <n>")
		}
		revision, err := workflowRevision(map[string]string{"--revision": args[4]})
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		input = contentlibrary.PublishRequest{ExpectedRevision: revision, Version: args[2]}
		method = http.MethodPost
		endpoint += "/" + url.PathEscape(args[1]) + "/publish"
	case "duplicate":
		if len(args) != 3 {
			return fail("expected content duplicate <id> <new-name>")
		}
		input = map[string]string{"name": args[2]}
		method = http.MethodPost
		endpoint += "/" + url.PathEscape(args[1]) + "/duplicate"
	case "archive", "restore":
		if len(args) != 2 {
			return fail("expected content archive|restore <id>")
		}
		input = struct{}{}
		method = http.MethodPost
		endpoint += "/" + url.PathEscape(args[1]) + "/" + args[0]
	default:
		return fail(fmt.Sprintf("unknown content command %q", args[0]))
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var result json.RawMessage
	if err := session.DoJSON(context.Background(), method, endpoint, input, &result); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeWorkflowResult(result, string(result), false, jsonOutput, stdout, stderr, command)
}
