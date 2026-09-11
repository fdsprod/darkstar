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
	"path/filepath"
	"strconv"
	"strings"

	workflowfilesystem "darkstar/src/adapters/workflowstore/filesystem"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/workflowstore"
)

type workflowMachineOutput struct {
	SchemaVersion int `json:"schemaVersion"`
	Result        any `json:"result"`
}

type workflowCandidateInput struct {
	Document        json.RawMessage     `json:"document"`
	SourceScope     workflowstore.Scope `json:"sourceScope"`
	SourceReference string              `json:"sourceReference"`
}

type workflowPreviewInput struct {
	Range   workflow.RouteRequest `json:"range"`
	Context workflow.RouteContext `json:"context"`
}

type workflowDraftCreateInput struct {
	Name           string                   `json:"name"`
	Scope          workflowstore.DraftScope `json:"scope"`
	ScopeReference string                   `json:"scopeReference"`
	Document       json.RawMessage          `json:"document"`
	Layout         json.RawMessage          `json:"layout,omitempty"`
}

type workflowDraftUpdateInput struct {
	ID               string          `json:"id"`
	ExpectedRevision uint64          `json:"expectedRevision"`
	Document         json.RawMessage `json:"document,omitempty"`
	Layout           json.RawMessage `json:"layout,omitempty"`
}
type workflowDraftRevisionInput struct {
	ID               string `json:"id"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}
type workflowDraftPublishInput struct {
	ID               string `json:"id"`
	Version          string `json:"version"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}
type workflowDraftPreviewInput struct {
	ID               string                `json:"id"`
	ExpectedRevision uint64                `json:"expectedRevision"`
	Range            workflow.RouteRequest `json:"range"`
	Context          workflow.RouteContext `json:"context"`
}
type workflowDraftDuplicateInput struct {
	Name           string                   `json:"name"`
	Version        string                   `json:"version,omitempty"`
	NewName        string                   `json:"newName"`
	Scope          workflowstore.DraftScope `json:"scope"`
	ScopeReference string                   `json:"scopeReference"`
}
type workflowDraftRenameInput struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}
type workflowArchiveInput struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func runWorkflow(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return workflowArgumentError(stdout, stderr, jsonOutput, "darkstar workflow", errors.New("a workflow command is required"))
	}
	command := "darkstar workflow " + args[0]
	switch args[0] {
	case "assessment-router":
		if len(args) != 2 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected assessment-router <request.json>"))
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var input workflow.AssessmentRouterPattern
		if err := json.Unmarshal(data, &input); err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		result, err := workflow.BuildAssessmentRouter(input)
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkflowResult(result, "Assessment router pattern created. Save the returned document to a workflow draft.", false, jsonOutput, stdout, stderr, command)
	case "node-definition":
		return runNodeDefinition(args[1:], jsonOutput, stdout, stderr)
	case "content-usage":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected workflow content-usage <content-id>"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result struct {
			Usages []workflow.ContentUsage `json:"usages"`
		}
		if err := session.DoJSON(context.Background(), http.MethodGet, "workflows/content-usage?contentId="+url.QueryEscape(args[1]), nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		_, _ = fmt.Fprintf(&human, "%d workflow reference(s).", len(result.Usages))
		for _, usage := range result.Usages {
			location := "node " + usage.NodeID
			if usage.InputID != "" {
				location = "input " + usage.InputID
			}
			_, _ = fmt.Fprintf(&human, "\n%s %s (%s) / %s -> %s", usage.WorkflowName, usage.WorkflowVersion, usage.Kind, location, usage.Reference.Version)
		}
		return writeWorkflowResult(result, human.String(), false, jsonOutput, stdout, stderr, command)
	case "authoring-catalog":
		if len(args) != 1 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected workflow authoring-catalog"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result workflow.AuthoringCatalog
		if err := session.DoJSON(context.Background(), http.MethodGet, "workflows/authoring-catalog", nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		workflowCount := 0
		if result.Workflows.Status == workflow.ReferenceKnown {
			workflowCount = len(result.Workflows.Items)
		}
		return writeWorkflowResult(result, fmt.Sprintf("%d active installed workflow reference(s); agent profile discovery is %s.", workflowCount, result.Agents.Status), false, jsonOutput, stdout, stderr, command)
	case "library":
		if len(args) != 1 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected workflow library"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result workflow.Library
		if err := session.DoJSON(context.Background(), http.MethodGet, "workflows/library", nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		human := fmt.Sprintf("%d installed version(s), %d draft(s).", len(result.Versions), len(result.Drafts))
		return writeWorkflowResult(result, human, false, jsonOutput, stdout, stderr, command)
	case "duplicate":
		if len(args) < 3 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <name> <new-name> --version <version> --scope <user|project> --scope-reference <reference> --idempotency-key <key>"))
		}
		flags, err := workflowFlags(args[3:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		input := workflowDraftDuplicateInput{Name: args[1], NewName: args[2], Version: flags["--version"], Scope: workflowstore.DraftScope(flags["--scope"]), ScopeReference: flags["--scope-reference"]}
		var result workflowstore.Draft
		if code := doWorkflowMutation(command, "workflows/drafts/duplicate", flags["--idempotency-key"], input, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, fmt.Sprintf("Duplicated %s as draft %s.", args[1], result.ID), false, jsonOutput, stdout, stderr, command)
	case "archive":
		if len(args) != 3 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <name> <version>"))
		}
		var result workflowstore.Archive
		if code := doWorkflowMutation(command, "workflows/archive", "", workflowArchiveInput{Name: args[1], Version: args[2]}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, fmt.Sprintf("Archived %s %s.", result.Name, result.Version), false, jsonOutput, stdout, stderr, command)
	case "draft-create":
		if len(args) < 2 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <file> --scope <user|project> --scope-reference <reference> --idempotency-key <key>"))
		}
		candidate, err := readWorkflowCandidate(args[1])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		flags, err := workflowFlags(args[2:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var metadata struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(candidate.Document, &metadata); err != nil || metadata.Metadata.Name == "" {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("workflow document metadata.name is required"))
		}
		scope := workflowstore.DraftScope(flags["--scope"])
		input := workflowDraftCreateInput{Name: metadata.Metadata.Name, Scope: scope, ScopeReference: flags["--scope-reference"], Document: candidate.Document}
		var result workflowstore.Draft
		if code := doWorkflowMutation(command, "workflows/drafts/create", flags["--idempotency-key"], input, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, fmt.Sprintf("Created draft %s at revision %d.", result.ID, result.Revision), false, jsonOutput, stdout, stderr, command)
	case "draft-show":
		if len(args) != 2 || args[1] == "" {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <draft-id>"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result workflowstore.Draft
		if err := session.DoJSON(context.Background(), http.MethodGet, "workflows/drafts/show?id="+url.QueryEscape(args[1]), nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkflowResult(result, fmt.Sprintf("%s %s revision %d", result.ID, result.Name, result.Revision), false, jsonOutput, stdout, stderr, command)
	case "draft-update":
		if len(args) < 3 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <draft-id> <file> --revision <n>"))
		}
		candidate, err := readWorkflowCandidate(args[2])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		flags, err := workflowFlags(args[3:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		revision, err := workflowRevision(flags)
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		input := workflowDraftUpdateInput{ID: args[1], ExpectedRevision: revision, Document: candidate.Document}
		var result workflowstore.Draft
		if code := doWorkflowMutation(command, "workflows/drafts/update", "", input, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, fmt.Sprintf("Saved draft %s at revision %d.", result.ID, result.Revision), false, jsonOutput, stdout, stderr, command)
	case "draft-rename":
		if len(args) < 3 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <draft-id> <name> --revision <n>"))
		}
		flags, err := workflowFlags(args[3:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		revision, err := workflowRevision(flags)
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result workflowstore.Draft
		if code := doWorkflowMutation(command, "workflows/drafts/rename", "", workflowDraftRenameInput{ID: args[1], Name: args[2], ExpectedRevision: revision}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, fmt.Sprintf("Renamed draft %s to %s at revision %d.", result.ID, result.Name, result.Revision), false, jsonOutput, stdout, stderr, command)
	case "draft-validate":
		id, revision, err := parseDraftRevisionArgs(args[1:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result workflow.DraftValidationReport
		if code := doWorkflowMutation(command, "workflows/drafts/validate", "", workflowDraftRevisionInput{ID: id, ExpectedRevision: revision}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		human := fmt.Sprintf("Draft %s revision %d has %d finding(s).", id, revision, len(result.Findings))
		return writeWorkflowResult(result, human, len(result.Findings) != 0, jsonOutput, stdout, stderr, command)
	case "draft-preview":
		id, revision, route, routeContext, err := parseDraftPreviewArgs(args[1:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result workflow.DraftPreview
		if code := doWorkflowMutation(command, "workflows/drafts/preview", "", workflowDraftPreviewInput{ID: id, ExpectedRevision: revision, Range: route, Context: routeContext}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, fmt.Sprintf("Previewed draft %s revision %d with %d route node(s).", id, revision, len(result.Route.Nodes)), false, jsonOutput, stdout, stderr, command)
	case "draft-publish":
		if len(args) < 3 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <draft-id> <version> --revision <n>"))
		}
		flags, err := workflowFlags(args[3:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		revision, err := workflowRevision(flags)
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result workflow.DraftPublishResult
		if code := doWorkflowMutation(command, "workflows/drafts/publish", "", workflowDraftPublishInput{ID: args[1], Version: args[2], ExpectedRevision: revision}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, fmt.Sprintf("Published %s %s (%s).", result.Published.Name, result.Published.Version, result.Published.Digest), false, jsonOutput, stdout, stderr, command)
	case "draft-discard":
		id, revision, err := parseDraftRevisionArgs(args[1:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result any
		if code := doWorkflowMutation(command, "workflows/drafts/discard", "", workflowDraftRevisionInput{ID: id, ExpectedRevision: revision}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(map[string]any{"id": id, "discarded": true}, "Discarded draft "+id+".", false, jsonOutput, stdout, stderr, command)
	case "validate", "install":
		if len(args) != 2 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("expected workflow %s <file>", args[0]))
		}
		candidate, err := readWorkflowCandidate(args[1])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		if args[0] == "validate" {
			var result workflow.ValidationReport
			if err := session.DoJSON(context.Background(), http.MethodPost, "workflows/validate", candidate, &result); err != nil {
				return writeClientError(stdout, stderr, jsonOutput, command, err)
			}
			human := "Workflow is valid."
			if len(result.Issues) != 0 {
				var lines strings.Builder
				_, _ = fmt.Fprintf(&lines, "Workflow validation found %d issue(s):\n", len(result.Issues))
				for _, issue := range result.Issues {
					_, _ = fmt.Fprintf(&lines, "- %s\n", issue.Error())
				}
				human = strings.TrimSuffix(lines.String(), "\n")
			}
			return writeWorkflowResult(result, human, len(result.Issues) != 0, jsonOutput, stdout, stderr, command)
		}
		var result workflow.InstallResult
		if err := session.DoJSON(context.Background(), http.MethodPost, "workflows/install", candidate, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		action := "Installed"
		if result.Disposition == workflow.InstallAlreadyInstalled {
			action = "Already installed"
		} else if result.Disposition != workflow.InstallCreated {
			return writeCommandError(stdout, stderr, jsonOutput, command, "INTERNAL_INVARIANT_VIOLATION", fmt.Sprintf("unknown workflow install disposition %q", result.Disposition), false, ExitInvariantViolation)
		}
		return writeWorkflowResult(result, fmt.Sprintf("%s %s %s (%s).", action, result.Version.Name, result.Version.Version, result.Version.Digest), false, jsonOutput, stdout, stderr, command)
	case "list":
		if len(args) != 1 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected workflow list"))
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var result []workflow.VersionSummary
		if err := session.DoJSON(context.Background(), http.MethodGet, "workflows", nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		for _, item := range result {
			_, _ = fmt.Fprintf(&human, "%s %s %s %s\n", item.Name, item.Version, item.SourceScope, item.Digest)
		}
		return writeWorkflowResult(result, strings.TrimSuffix(human.String(), "\n"), false, jsonOutput, stdout, stderr, command)
	case "show", "graph":
		name, version, err := parseWorkflowIdentityArgs(args[1:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		endpoint := "workflows/" + args[0] + "?name=" + url.QueryEscape(name)
		if version != "" {
			endpoint += "&version=" + url.QueryEscape(version)
		}
		if args[0] == "show" {
			var result workflow.Definition
			if err := session.DoJSON(context.Background(), http.MethodGet, endpoint, nil, &result); err != nil {
				return writeClientError(stdout, stderr, jsonOutput, command, err)
			}
			encoded, err := workflow.Encode(result.Document)
			if err != nil {
				return writeCommandError(stdout, stderr, jsonOutput, command, "INTERNAL_INVARIANT_VIOLATION", err.Error(), false, ExitInvariantViolation)
			}
			human := fmt.Sprintf("%s %s (%s)\n%s", result.Version.Name, result.Version.Version, result.Version.Digest, encoded)
			return writeWorkflowResult(result, strings.TrimSuffix(human, "\n"), false, jsonOutput, stdout, stderr, command)
		}
		var result workflow.Graph
		if err := session.DoJSON(context.Background(), http.MethodGet, endpoint, nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkflowResult(result, formatWorkflowGraph(result), false, jsonOutput, stdout, stderr, command)
	case "preview":
		name, version, routeRequest, routeContext, err := parseWorkflowPreviewArgs(args[1:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		endpoint := "workflows/preview?name=" + url.QueryEscape(name)
		if version != "" {
			endpoint += "&version=" + url.QueryEscape(version)
		}
		var result workflow.RoutePreview
		if err := session.DoJSON(context.Background(), http.MethodPost, endpoint, workflowPreviewInput{Range: routeRequest, Context: routeContext}, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkflowResult(result, formatRoutePreview(result), false, jsonOutput, stdout, stderr, command)
	default:
		return workflowArgumentError(stdout, stderr, jsonOutput, "darkstar workflow", fmt.Errorf("unknown workflow command %q", args[0]))
	}
}

func runNodeDefinition(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	command := "darkstar workflow node-definition"
	if len(args) == 0 {
		return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("a node-definition command is required"))
	}
	command += " " + args[0]
	read := func(file string, destination any) error {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		return decoder.Decode(destination)
	}
	switch args[0] {
	case "list":
		flags, err := workflowFlags(args[1:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		query := url.Values{}
		for flag, key := range map[string]string{"--query": "query", "--scope": "scope", "--lifecycle": "lifecycle"} {
			if flags[flag] != "" {
				query.Set(key, flags[flag])
			}
		}
		endpoint := "workflows/node-definitions"
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
		var result []workflow.NodeDefinition
		if err := session.DoJSON(context.Background(), http.MethodGet, endpoint, nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeWorkflowResult(result, fmt.Sprintf("%d node definition(s).", len(result)), false, jsonOutput, stdout, stderr, command)
	case "create":
		if len(args) < 2 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <definition.json> --idempotency-key <key>"))
		}
		flags, err := workflowFlags(args[2:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var input workflow.NodeDefinitionCreateRequest
		if err := read(args[1], &input); err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result workflow.NodeDefinition
		if code := doWorkflowMutation(command, "workflows/node-definitions/create", flags["--idempotency-key"], input, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, "Created node definition.", false, jsonOutput, stdout, stderr, command)
	case "duplicate":
		if len(args) < 3 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <ref.json> <name> with scope, owner, version, and idempotency key"))
		}
		flags, err := workflowFlags(args[3:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var source workflow.ResolvedNodeDefinitionRef
		if err := read(args[1], &source); err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		input := map[string]any{"source": source, "scope": flags["--scope"], "owner": flags["--owner"], "name": args[2], "version": flags["--version"]}
		var result workflow.NodeDefinition
		if code := doWorkflowMutation(command, "workflows/node-definitions/duplicate", flags["--idempotency-key"], input, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, "Duplicated node definition.", false, jsonOutput, stdout, stderr, command)
	case "version":
		if len(args) < 3 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <ref.json> <version> --idempotency-key <key>"))
		}
		flags, err := workflowFlags(args[3:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var source workflow.ResolvedNodeDefinitionRef
		if err := read(args[1], &source); err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result workflow.NodeDefinition
		if code := doWorkflowMutation(command, "workflows/node-definitions/version", flags["--idempotency-key"], map[string]any{"source": source, "version": args[2]}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, "Versioned node definition.", false, jsonOutput, stdout, stderr, command)
	case "archive":
		if len(args) < 2 {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected <ref.json> --idempotency-key <key>"))
		}
		flags, err := workflowFlags(args[2:])
		if err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var ref workflow.ResolvedNodeDefinitionRef
		if err := read(args[1], &ref); err != nil {
			return workflowArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result workflow.NodeDefinition
		if code := doWorkflowMutation(command, "workflows/node-definitions/archive", flags["--idempotency-key"], map[string]any{"ref": ref}, &result, jsonOutput, stdout, stderr); code != -1 {
			return code
		}
		return writeWorkflowResult(result, "Archived node definition.", false, jsonOutput, stdout, stderr, command)
	default:
		return workflowArgumentError(stdout, stderr, jsonOutput, command, fmt.Errorf("unknown node-definition command %q", args[0]))
	}
}

func workflowFlags(args []string) (map[string]string, error) {
	if len(args)%2 != 0 {
		return nil, errors.New("workflow options require values")
	}
	result := make(map[string]string, len(args)/2)
	for index := 0; index < len(args); index += 2 {
		if !strings.HasPrefix(args[index], "--") || args[index+1] == "" {
			return nil, fmt.Errorf("invalid workflow option %q", args[index])
		}
		if _, exists := result[args[index]]; exists {
			return nil, fmt.Errorf("%s may be specified only once", args[index])
		}
		result[args[index]] = args[index+1]
	}
	return result, nil
}

func workflowRevision(flags map[string]string) (uint64, error) {
	value, err := strconv.ParseUint(flags["--revision"], 10, 64)
	if err != nil || value == 0 {
		return 0, errors.New("--revision requires a positive integer")
	}
	return value, nil
}

func parseDraftRevisionArgs(args []string) (string, uint64, error) {
	if len(args) < 1 || args[0] == "" {
		return "", 0, errors.New("expected <draft-id> --revision <n>")
	}
	flags, err := workflowFlags(args[1:])
	if err != nil {
		return "", 0, err
	}
	revision, err := workflowRevision(flags)
	return args[0], revision, err
}

func parseDraftPreviewArgs(args []string) (string, uint64, workflow.RouteRequest, workflow.RouteContext, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", 0, workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("expected <draft-id> --revision <n> with optional --from, --until, and --input")
	}
	previewArgs := []string{args[0]}
	var revision uint64
	seenRevision := false
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return "", 0, workflow.RouteRequest{}, workflow.RouteContext{}, fmt.Errorf("%s requires a value", args[index])
		}
		if args[index] == "--revision" {
			if seenRevision {
				return "", 0, workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("--revision may be specified only once")
			}
			seenRevision = true
			value, err := strconv.ParseUint(args[index+1], 10, 64)
			if err != nil || value == 0 {
				return "", 0, workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("--revision requires a positive integer")
			}
			revision = value
		} else {
			previewArgs = append(previewArgs, args[index], args[index+1])
		}
	}
	if !seenRevision {
		return "", 0, workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("--revision requires a positive integer")
	}
	id, version, route, routeContext, err := parseWorkflowPreviewArgs(previewArgs)
	if err != nil {
		return "", 0, workflow.RouteRequest{}, workflow.RouteContext{}, err
	}
	if version != "" {
		return "", 0, workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("--version is not valid for a draft preview")
	}
	return id, revision, route, routeContext, nil
}

// doWorkflowMutation returns -1 on success so callers can format their typed result.
func doWorkflowMutation(command, endpoint, key string, input, result any, jsonOutput bool, stdout, stderr io.Writer) int {
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var err error
	if key != "" {
		err = session.DoJSON(context.Background(), http.MethodPost, endpoint, input, result, clientHeader("Idempotency-Key", key))
	} else {
		err = session.DoJSON(context.Background(), http.MethodPost, endpoint, input, result)
	}
	if err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return -1
}

func readWorkflowCandidate(path string) (workflowCandidateInput, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return workflowCandidateInput{}, fmt.Errorf("resolve workflow file: %w", err)
	}
	document, err := workflowfilesystem.ReadDocument(absolute)
	if err != nil {
		return workflowCandidateInput{}, fmt.Errorf("read workflow file: %w", err)
	}
	return workflowCandidateInput{Document: document, SourceScope: workflowstore.ScopeProject, SourceReference: absolute}, nil
}

func parseWorkflowIdentityArgs(args []string) (string, string, error) {
	if len(args) != 1 && len(args) != 3 {
		return "", "", errors.New("expected <name> [--version <version>]")
	}
	if strings.TrimSpace(args[0]) == "" {
		return "", "", errors.New("workflow name is required")
	}
	if len(args) == 3 && (args[1] != "--version" || args[2] == "") {
		return "", "", errors.New("--version requires a semantic version")
	}
	if len(args) == 3 {
		return args[0], args[2], nil
	}
	return args[0], "", nil
}

func parseWorkflowPreviewArgs(args []string) (string, string, workflow.RouteRequest, workflow.RouteContext, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", "", workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("expected <name> with optional --version, --from, --until, and --input")
	}
	name, version := args[0], ""
	request := workflow.RouteRequest{}
	contextValue := workflow.RouteContext{}
	seenVersion, seenFrom, seenInput := false, false, false
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return "", "", workflow.RouteRequest{}, workflow.RouteContext{}, fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--version":
			if seenVersion {
				return "", "", workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("--version may be specified only once")
			}
			seenVersion, version = true, value
		case "--from":
			if seenFrom {
				return "", "", workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("--from may be specified only once")
			}
			seenFrom, request.From = true, workflow.Identifier(value)
		case "--until":
			request.Until = append(request.Until, workflow.Identifier(value))
		case "--input":
			if seenInput {
				return "", "", workflow.RouteRequest{}, workflow.RouteContext{}, errors.New("--input may be specified only once")
			}
			seenInput = true
			inputs, err := readWorkflowInputs(value)
			if err != nil {
				return "", "", workflow.RouteRequest{}, workflow.RouteContext{}, err
			}
			contextValue.RunInputs = inputs
		default:
			return "", "", workflow.RouteRequest{}, workflow.RouteContext{}, fmt.Errorf("unknown preview option %q", args[index])
		}
	}
	return name, version, request, contextValue, nil
}

func readWorkflowInputs(path string) (map[workflow.Identifier]json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow input file: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	content, err := io.ReadAll(io.LimitReader(file, workflowfilesystem.MaxDocumentSize+1))
	if err != nil {
		return nil, fmt.Errorf("read workflow input file: %w", err)
	}
	if len(content) > workflowfilesystem.MaxDocumentSize {
		return nil, fmt.Errorf("workflow input file exceeds %d bytes", workflowfilesystem.MaxDocumentSize)
	}
	var inputs map[workflow.Identifier]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(&inputs); err != nil || inputs == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("workflow input file must contain one JSON object")
	}
	return inputs, nil
}

func formatWorkflowGraph(graph workflow.Graph) string {
	var output strings.Builder
	_, _ = fmt.Fprintf(&output, "%s %s\n", graph.Workflow.Name, graph.Workflow.Version)
	for _, node := range graph.Nodes {
		labels := []string{string(node.Type)}
		if node.Entry {
			labels = append(labels, "entry")
		}
		if node.Terminal {
			labels = append(labels, "terminal")
		}
		_, _ = fmt.Fprintf(&output, "- %s [%s]\n", node.ID, strings.Join(labels, ", "))
		for _, edge := range graph.Edges {
			if edge.From == node.ID {
				_, _ = fmt.Fprintf(&output, "  -> %s (%s)\n", edge.To, edge.ID)
			}
		}
	}
	return strings.TrimSuffix(output.String(), "\n")
}

func formatRoutePreview(preview workflow.RoutePreview) string {
	var output strings.Builder
	_, _ = fmt.Fprintf(&output, "%s %s\nEntry: %s\nTerminals: ", preview.Workflow.Name, preview.Workflow.Version, preview.Route.Entry)
	for index, terminal := range preview.Route.Terminals {
		if index != 0 {
			_, _ = output.WriteString(", ")
		}
		_, _ = output.WriteString(string(terminal))
	}
	_, _ = output.WriteString("\nNodes:\n")
	for _, node := range preview.Route.Nodes {
		_, _ = fmt.Fprintf(&output, "- %s\n", node.ID)
	}
	if len(preview.Route.ExcludedNodes) != 0 {
		_, _ = output.WriteString("Excluded:\n")
		for _, node := range preview.Route.ExcludedNodes {
			_, _ = fmt.Fprintf(&output, "- %s (%s)\n", node.ID, node.Reason)
		}
	}
	if len(preview.Route.InputRequirements) != 0 {
		_, _ = output.WriteString("Inputs required:\n")
		for _, requirement := range preview.Route.InputRequirements {
			_, _ = fmt.Fprintf(&output, "- %s.%s <- %s\n", requirement.Node, requirement.Input, requirement.Source)
		}
	}
	return strings.TrimSuffix(output.String(), "\n")
}

func writeWorkflowResult(result any, human string, validationFailed, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, workflowMachineOutput{SchemaVersion: machineSchemaVersion, Result: result}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else if human != "" {
		_, _ = fmt.Fprintln(stdout, human)
	}
	if validationFailed {
		return int(ExitValidationFailed)
	}
	return int(ExitSuccess)
}

func workflowArgumentError(stdout, stderr io.Writer, jsonOutput bool, command string, err error) int {
	return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
}
