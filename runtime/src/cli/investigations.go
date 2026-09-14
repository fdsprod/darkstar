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
	"strconv"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/investigation"
)

var investigationIdentityPattern = regexp.MustCompile(`^investigation_[0-9A-HJKMNP-TV-Z]{26}$`)
var investigationArtifactPattern = regexp.MustCompile(`^artifact_.+$`)
var investigationDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type investigationCommand struct {
	method   string
	resource string
	body     any
	key      string
	revision uint64
}

func runInvestigation(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	const command = "darkstar investigation"
	parsed, err := parseInvestigationCommand(args)
	if err != nil {
		return workArgumentError(stdout, stderr, jsonOutput, command, err)
	}
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var options []clientapi.RequestOption
	if parsed.key != "" {
		options = append(options, clientapi.WithHeader("Idempotency-Key", parsed.key))
	}
	if parsed.revision != 0 {
		options = append(options, clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, parsed.revision)))
	}
	var result investigation.View
	if err := session.DoJSON(context.Background(), parsed.method, parsed.resource, parsed.body, &result, options...); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return writeWorkResult(result, investigationSummary(result), jsonOutput, stdout, stderr, command)
}

func parseInvestigationCommand(args []string) (investigationCommand, error) {
	invalid := func(message string) (investigationCommand, error) {
		return investigationCommand{}, errors.New(message)
	}
	if len(args) < 2 {
		return invalid("expected investigation prepare <request.json>, show <id>, or start|retry|cancel <id> --revision <n>")
	}
	action, target := args[0], args[1]
	flags := map[string]string{}
	for index := 2; index < len(args); index += 2 {
		if index+1 == len(args) || (args[index] != "--idempotency-key" && args[index] != "--revision") {
			return invalid("investigation flags require one value each: --idempotency-key or --revision")
		}
		if _, repeated := flags[args[index]]; repeated {
			return invalid("investigation flags cannot be repeated")
		}
		flags[args[index]] = args[index+1]
	}
	parsed := investigationCommand{method: http.MethodPost, resource: "investigations"}
	switch action {
	case "prepare":
		if _, supplied := flags["--revision"]; supplied {
			return invalid("prepare creates an immutable investigation; --revision applies only to start, retry, and cancel")
		}
		input, err := readInvestigationPrepareRequest(target)
		if err != nil {
			return investigationCommand{}, err
		}
		parsed.body = input
	case "show":
		if len(flags) != 0 || !investigationIdentityPattern.MatchString(target) {
			return invalid("show requires one investigation ID and no mutation flags")
		}
		return investigationCommand{method: http.MethodGet, resource: "investigations/" + target}, nil
	case "start", "retry", "cancel":
		if !investigationIdentityPattern.MatchString(target) {
			return invalid("a canonical investigation ID is required")
		}
		version, err := strconv.ParseUint(flags["--revision"], 10, 64)
		if err != nil || version == 0 {
			return invalid("--revision must be the positive collection revision returned by investigation show")
		}
		parsed.resource += "/" + target + "/" + action
		parsed.body = struct{}{}
		parsed.revision = version
	default:
		return invalid("unknown investigation command; use prepare, show, start, retry, or cancel")
	}
	var supplied bool
	parsed.key, supplied = flags["--idempotency-key"]
	if !supplied {
		parsed.key = newIdempotencyKey()
	}
	if strings.TrimSpace(parsed.key) != parsed.key || len(parsed.key) < 8 || len(parsed.key) > 128 {
		return invalid("idempotency key must be 8 to 128 bytes without surrounding whitespace")
	}
	return parsed, nil
}

func readInvestigationPrepareRequest(filename string) (investigation.PrepareRequest, error) {
	var request investigation.PrepareRequest
	file, err := os.Open(filename)
	if err != nil {
		return request, fmt.Errorf("read investigation request: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return request, errors.New("investigation request cannot exceed 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		return request, errors.New("investigation request must be one JSON object with scopeId, task, and optional concurrency")
	}
	if !scopeIdentityPattern.MatchString(request.ScopeID) || request.Concurrency < 0 || request.Concurrency > 8 {
		return request, errors.New("investigation request requires a scope ID and concurrency from 1 to 8 (omit for the default)")
	}
	switch request.Task.Kind {
	case "text":
		if strings.TrimSpace(request.Task.Text) == "" || request.Task.FeatureBrief != nil {
			return request, errors.New("text task requires nonempty text and cannot contain a featureBrief reference")
		}
	case "feature_brief":
		if request.Task.Text != "" || request.Task.FeatureBrief == nil {
			return request, errors.New("feature_brief task requires an exact featureBrief reference and cannot contain text")
		}
		brief := request.Task.FeatureBrief
		if !investigationArtifactPattern.MatchString(brief.ArtifactID) || brief.Version == 0 || !investigationDigestPattern.MatchString(brief.SHA256) {
			return request, errors.New("featureBrief requires an artifactId, positive version, and lowercase SHA-256")
		}
	default:
		return request, errors.New("task kind must be text or feature_brief")
	}
	return request, nil
}

func investigationSummary(value investigation.View) string {
	var summary strings.Builder
	_, _ = fmt.Fprintf(&summary, "%s: %s (revision %d).", value.Collection.CollectionID, value.Collection.Status, value.Collection.Revision)
	for _, unit := range value.Units {
		label := unit.RepositoryID
		if unit.Kind == "synthesis" {
			label = "Cross-repository synthesis"
		}
		_, _ = fmt.Fprintf(&summary, "\n%s: %s", label, unit.Status)
		if unit.Reason != "" {
			_, _ = fmt.Fprintf(&summary, " — %s", unit.Reason)
		}
		if unit.Result != nil {
			_, _ = fmt.Fprintf(&summary, " (%s; %s version %d)", unit.Result.Quality, unit.Result.Artifact.ArtifactID, unit.Result.Artifact.Version)
		}
	}
	return summary.String()
}
