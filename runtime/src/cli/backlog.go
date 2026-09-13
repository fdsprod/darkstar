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
	"strconv"
	"strings"

	localapi "darkstar/src/api"
	"darkstar/src/ports/tracker"
)

func runBacklog(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar backlog"
	if len(args) < 2 || !projectIdentityPattern.MatchString(args[1]) {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected backlog list|source|select-native|select|refresh|refresh-ticket <project-id> with an exact project ID"))
	}
	command += " " + args[0]
	flags := make(map[string]string)
	for index := 2; index < len(args); index += 2 {
		if index+1 >= len(args) || !strings.HasPrefix(args[index], "--") {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("backlog flags require one value"))
		}
		if _, exists := flags[args[index]]; exists {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("repeated backlog flag"))
		}
		flags[args[index]] = args[index+1]
	}
	resource := "projects/" + url.PathEscape(args[1]) + "/backlog"
	method := http.MethodGet
	var input any
	allowed := make(map[string]bool)
	if args[0] != "list" && args[0] != "source" {
		allowed["--revision"] = true
		if revision, err := strconv.ParseUint(flags["--revision"], 10, 64); err != nil || revision == 0 {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--revision must name the current source binding revision; inspect backlog source first"))
		}
	}
	revision, _ := strconv.ParseUint(flags["--revision"], 10, 64)
	switch args[0] {
	case "list":
		query := url.Values{}
		for flag, parameter := range map[string]string{"--limit": "limit", "--cursor": "cursor", "--include-previous": "includePrevious"} {
			allowed[flag] = true
			if value, exists := flags[flag]; exists {
				query.Set(parameter, value)
			}
		}
		if len(query) != 0 {
			resource += "?" + query.Encode()
		}
	case "source":
		resource += "/source"
	case "select", "select-native":
		resource += "/source"
		method = http.MethodPut
		source := localapi.BacklogSource{Kind: "built_in"}
		if args[0] == "select" {
			allowed["--source-file"] = true
			if err := readBacklogJSON(flags["--source-file"], &source); err != nil {
				return workArgumentError(stdout, stderr, jsonOutput, command, err)
			}
		}
		input = localapi.BacklogSourceRequest{SchemaVersion: 1, ExpectedRevision: revision, Source: source}
	case "refresh":
		resource += "/refresh"
		method = http.MethodPost
		query := localapi.BacklogQuery{PageSize: 50, Predicates: []localapi.BacklogPredicate{}}
		for _, flag := range []string{"--query-file", "--search", "--state", "--page-size"} {
			allowed[flag] = true
		}
		if filename := flags["--query-file"]; filename != "" {
			if flags["--search"] != "" || flags["--state"] != "" || flags["--page-size"] != "" {
				return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--query-file cannot be combined with query flags"))
			}
			if err := readBacklogJSON(filename, &query); err != nil {
				return workArgumentError(stdout, stderr, jsonOutput, command, err)
			}
		} else {
			query.Text = flags["--search"]
			if state := flags["--state"]; state != "" {
				query.Predicates = append(query.Predicates, localapi.BacklogPredicate{FieldID: "business_state", Operator: tracker.Equals, Values: []string{state}})
			}
			if size := flags["--page-size"]; size != "" {
				parsed, err := strconv.Atoi(size)
				if err != nil || parsed < 1 || parsed > 100 {
					return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--page-size must be between 1 and 100"))
				}
				query.PageSize = parsed
			}
		}
		input = localapi.BacklogRefreshRequest{SchemaVersion: 1, ExpectedBindingRevision: revision, Query: query}
	case "refresh-ticket":
		allowed["--ref-file"] = true
		var ref localapi.BacklogTicketRef
		if err := readBacklogJSON(flags["--ref-file"], &ref); err != nil {
			return workArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		resource += "/refresh-ticket"
		method = http.MethodPost
		input = localapi.BacklogTicketRefreshRequest{SchemaVersion: 1, ExpectedBindingRevision: revision, Ref: ref}
	default:
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unsupported backlog operation"))
	}
	for flag := range flags {
		if !allowed[flag] {
			return workArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("unsupported backlog flag %s", flag))
		}
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	if args[0] == "list" {
		var page localapi.BacklogView
		if err := session.DoJSON(context.Background(), method, resource, nil, &page); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		fmt.Fprintf(&human, "Source revision %d\n", page.Binding.Revision)
		if page.Refresh != nil {
			fmt.Fprintf(&human, "Refresh: %s; last success: %s; next attempt: %s\n", page.Refresh.Phase, page.Refresh.LastSuccessAt, page.Refresh.NextAttemptAt)
			if page.Refresh.Error != nil {
				fmt.Fprintf(&human, "Source error: %s\n", page.Refresh.Error.Message)
			}
		}
		for _, ticket := range page.Tickets {
			fmt.Fprintf(&human, "%s %s: %s\n", ticket.Key, ticket.Status, ticket.Title)
			if !ticket.CurrentSource {
				fmt.Fprintf(&human, "  Retained from source revision %d\n", ticket.BindingRevision)
			} else if !ticket.CurrentQueryMatch {
				fmt.Fprintln(&human, "  Retained from an earlier filter or scan")
			}
		}
		if page.NextCursor != "" {
			fmt.Fprintf(&human, "Next cursor: %s\n", page.NextCursor)
		}
		return writeWorkResult(page, strings.TrimSpace(human.String()), jsonOutput, stdout, stderr, command)
	}
	if args[0] == "source" || args[0] == "select" || args[0] == "select-native" {
		var result localapi.BacklogSourceResponse
		if err := session.DoJSON(context.Background(), method, resource, input, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkResult(result, fmt.Sprintf("%s source, binding revision %d; %d retained selection(s)", result.Binding.Source.Kind, result.Binding.Revision, len(result.History)), jsonOutput, stdout, stderr, command)
	}
	var result localapi.BacklogRefreshResponse
	if err := session.DoJSON(context.Background(), method, resource, input, &result); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeWorkResult(result, fmt.Sprintf("Backlog refresh %s for source revision %d", result.Refresh.Phase, result.Refresh.BindingRevision), jsonOutput, stdout, stderr, command)
}

func readBacklogJSON(filename string, destination any) error {
	if filename == "" {
		return errors.New("a JSON input file is required")
	}
	file, err := os.Open(filename)
	if err != nil {
		return errors.New("cannot open backlog JSON input file")
	}
	defer func() {
		_ = file.Close()
	}()
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("backlog JSON input has unsupported fields or invalid values")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("backlog JSON input contains trailing content")
	}
	return nil
}
