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

	"darkstar/src/api"
	clientapi "darkstar/src/api/client"
)

func runTicketExecution(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar ticket-execution"
	if len(args) == 0 {
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected list, show, admit, approve, refresh, or rebind"))
	}
	command += " " + args[0]
	operation := args[0]
	identity := ""
	start := 1
	if operation != "list" {
		if len(args) < 2 || operation == "admit" && !projectIdentityPattern.MatchString(args[1]) || operation != "admit" && !workIdentityPattern.MatchString(args[1]) {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected the exact project ID for admission or work ID for source operations"))
		}
		identity, start = args[1], 2
	}
	flags := map[string]string{}
	for index := start; index < len(args); index += 2 {
		if index+1 >= len(args) || !strings.HasPrefix(args[index], "--") || flags[args[index]] != "" || args[index+1] == "" {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("source flags require one nonempty value and cannot repeat"))
		}
		flags[args[index]] = args[index+1]
	}
	allowed := map[string]bool{}
	resource, method := "work-items/"+identity+"/source", http.MethodGet
	var input any
	key := ""
	if operation == "admit" || operation == "approve" || operation == "rebind" {
		allowed["--idempotency-key"], allowed["--observation"] = true, true
		key = flags["--idempotency-key"]
		if key == "" {
			key = newIdempotencyKey()
		}
		if flags["--observation"] == "" {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--observation must name the exact saved ticket version being approved"))
		}
		method = http.MethodPost
	}
	revision := uint64(0)
	if operation == "admit" || operation == "rebind" {
		allowed["--revision"] = true
		var err error
		revision, err = strconv.ParseUint(flags["--revision"], 10, 64)
		if err != nil || revision == 0 {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--revision requires the exact selected source binding revision"))
		}
	}
	switch operation {
	case "list":
		allowed["--project"] = true
		resource = "work-items/source-views"
		if project := flags["--project"]; project != "" {
			if !projectIdentityPattern.MatchString(project) {
				return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--project requires an exact project ID"))
			}
			resource += "?projectId=" + url.QueryEscape(project)
		}
	case "show":
	case "refresh":
		resource += "/refresh"
		method = http.MethodPost
	case "admit":
		resource = "projects/" + identity + "/backlog/admit"
		input = api.TicketAdmissionRequest{SchemaVersion: 1, ExpectedBindingRevision: revision, ObservationID: flags["--observation"]}
	case "approve":
		resource += "/approve"
		input = api.SourceApprovalRequest{SchemaVersion: 1, ObservationID: flags["--observation"]}
	case "rebind":
		allowed["--lineage-revision"] = true
		lineage, err := strconv.ParseUint(flags["--lineage-revision"], 10, 64)
		if err != nil || lineage == 0 {
			return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("--lineage-revision requires the exact work source lineage revision"))
		}
		resource += "/rebind"
		input = api.SourceRebindRequest{SchemaVersion: 1, ExpectedBindingRevision: revision, ExpectedLineageRevision: lineage, ObservationID: flags["--observation"]}
	default:
		return workArgumentError(stdout, stderr, jsonOutput, command, errors.New("unsupported ticket execution operation"))
	}
	for flag := range flags {
		if !allowed[flag] {
			return workArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("unsupported source flag %s", flag))
		}
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	if operation == "list" {
		var result api.WorkSourceViews
		if err := session.DoJSON(context.Background(), method, resource, input, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkResult(result, fmt.Sprintf("%d retained work source views", len(result.Items)), jsonOutput, stdout, stderr, command)
	}
	if operation == "show" || operation == "refresh" {
		var result api.WorkSourceView
		if err := session.DoJSON(context.Background(), method, resource, input, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkResult(result, sourceExecutionText(result), jsonOutput, stdout, stderr, command)
	}
	var result api.TicketAdmissionResponse
	if err := session.DoJSON(context.Background(), method, resource, input, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeWorkResult(result, fmt.Sprintf("Approved source observation %s for %s", result.SourceObservationID, result.WorkItemID), jsonOutput, stdout, stderr, command)
}

func sourceExecutionText(view api.WorkSourceView) string {
	var output strings.Builder
	_, _ = fmt.Fprintf(&output, "%s: source %s; local activity %s; run outcome %s\n", view.WorkItemID, view.Assessment.State, view.LocalActivity, view.RunOutcome)
	if view.CurrentTicket != nil {
		_, _ = fmt.Fprintf(&output, "Current source: %s — %s\n", view.CurrentTicket.Key, view.CurrentTicket.Title)
		state := view.CurrentTicket.BusinessState
		if state.Value != nil {
			_, _ = fmt.Fprintf(&output, "Business status: %s\n", state.Value.Name)
		} else {
			_, _ = fmt.Fprintf(&output, "Business status: %s (%s)\n", state.State, state.Reason)
		}
	}
	if view.Approval != nil {
		_, _ = fmt.Fprintf(&output, "Approved observation: %s\n", view.Approval.ObservationID)
	}
	for _, reason := range view.Assessment.Reasons {
		_, _ = fmt.Fprintln(&output, reason)
	}
	_, _ = fmt.Fprintf(&output, "External acceptance: %s", view.ExternalAcceptance.State)
	if view.ExternalAcceptance.Value != nil {
		_, _ = fmt.Fprintf(&output, " (%s)", *view.ExternalAcceptance.Value)
	} else if view.ExternalAcceptance.Reason != "" {
		_, _ = fmt.Fprintf(&output, " (%s)", view.ExternalAcceptance.Reason)
	}
	return output.String()
}
