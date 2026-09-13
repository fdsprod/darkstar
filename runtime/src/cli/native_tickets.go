package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/ticketmanagement"
)

func runTicket(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar ticket"
	if len(args) < 2 {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected ticket list <project-id>, show <project-id> <ticket-id>, edit <project-id> <ticket-id> --revision <revision> --idempotency-key <key> --title <title>, or transition <project-id> <ticket-id> --revision <revision> --idempotency-key <key> --transition <id>"))
	}
	command += " " + args[0]
	if !projectIdentityPattern.MatchString(args[1]) {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("a canonical project ID is required"))
	}
	resource := "projects/" + url.PathEscape(args[1]) + "/tickets"
	method := http.MethodGet
	var input any
	key := ""
	flags := map[string]string{}
	start := 2
	if args[0] != "list" {
		if len(args) < 3 || args[2] == "" || strings.ContainsAny(args[2], "/\\") {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("a ticket ID is required"))
		}
		resource += "/" + url.PathEscape(args[2])
		start = 3
	}
	for index := start; index < len(args); index += 2 {
		if index+1 >= len(args) || !strings.HasPrefix(args[index], "--") {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("flags require one value"))
		}
		if _, exists := flags[args[index]]; exists {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("repeated flag"))
		}
		flags[args[index]] = args[index+1]
	}
	allowed := map[string]bool{}
	switch args[0] {
	case "list":
		query := url.Values{}
		for flag, parameter := range map[string]string{"--search": "q", "--state": "state", "--cursor": "cursor", "--page-size": "pageSize"} {
			allowed[flag] = true
			if value, exists := flags[flag]; exists {
				query.Set(parameter, value)
			}
		}
		if len(query) != 0 {
			resource += "?" + query.Encode()
		}
	case "show":
	case "edit", "transition":
		method = http.MethodPost
		resource += "/" + args[0]
		allowed["--revision"] = true
		allowed["--idempotency-key"] = true
		key = flags["--idempotency-key"]
		if flags["--revision"] == "" || key == "" {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--revision and --idempotency-key are required; inspect ticket show first"))
		}
		if args[0] == "transition" {
			allowed["--transition"] = true
			input = ticketmanagement.TransitionRequest{SchemaVersion: 1, Revision: flags["--revision"], TransitionID: flags["--transition"]}
		} else {
			request := ticketmanagement.EditRequest{SchemaVersion: 1, Revision: flags["--revision"]}
			for flag, destination := range map[string]**string{"--title": &request.Title, "--description": &request.Description} {
				allowed[flag] = true
				if value, exists := flags[flag]; exists {
					copy := value
					*destination = &copy
				}
			}
			allowed["--priority"] = true
			if value, exists := flags["--priority"]; exists {
				priority, err := strconv.Atoi(value)
				if err != nil || priority < 0 {
					return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("priority must be a nonnegative integer"))
				}
				request.Priority = &priority
			}
			input = request
		}
	default:
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unknown ticket command"))
	}
	for flag := range flags {
		if !allowed[flag] {
			return workArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("unsupported flag %s", flag))
		}
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	if args[0] == "list" {
		var page ticketmanagement.Page
		if err := session.DoJSON(context.Background(), method, resource, nil, &page); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		for _, ticket := range page.Tickets {
			fmt.Fprintf(&human, "%s %s %s (revision %s)\n", ticket.ID, ticket.BusinessState.Name, ticket.Title, ticket.Revision)
		}
		if page.NextCursor != "" {
			fmt.Fprintf(&human, "Next cursor: %s\n", page.NextCursor)
		}
		return writeWorkResult(page, strings.TrimSpace(human.String()), jsonOutput, stdout, stderr, command)
	}
	var detail ticketmanagement.Detail
	if err := session.DoJSON(context.Background(), method, resource, input, &detail, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	var human strings.Builder
	fmt.Fprintf(&human, "%s: %s\nBusiness state: %s; revision %s\n%s\n", detail.Ticket.ID, detail.Ticket.Title, detail.Ticket.BusinessState.Name, detail.Ticket.Revision, detail.Ticket.Description)
	for _, transition := range detail.Transitions {
		fmt.Fprintf(&human, "Transition %s: %s\n", transition.ID, transition.Name)
	}
	for _, entry := range detail.History {
		fmt.Fprintf(&human, "History %s: %s, %s, %s\n", entry.Revision, entry.Kind, entry.BusinessState, entry.Title)
	}
	return writeWorkResult(detail, strings.TrimSpace(human.String()), jsonOutput, stdout, stderr, command)
}
