package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/impactassessment"
	"darkstar/src/ports/representationregistry"
)

const maxCLIArtifactBytes = 25 << 20

type artifactMachineOutput struct {
	SchemaVersion int `json:"schemaVersion"`
	Result        any `json:"result"`
}

func runArtifact(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar artifact", "ARGUMENT_INVALID", "an artifact command is required", false, ExitInvalidInput)
	}
	command := "darkstar artifact " + args[0]
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	ctx := context.Background()
	switch args[0] {
	case "ingest":
		input, err := parseArtifactContent(args[1:])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result artifactingest.Result
		err = session.DoJSON(ctx, http.MethodPost, "artifacts", input, &result, clientHeader("Idempotency-Key", newIdempotencyKey()))
		if err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeArtifactResult(result, fmt.Sprintf("Ingested %s@%d.", result.Artifact.ArtifactID, result.Artifact.Version), false, jsonOutput, stdout, stderr, command)
	case "revise":
		if len(args) < 3 {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected artifact revise <artifact-id>@<base-version> with one content source"))
		}
		base, err := parseArtifactReference(args[1])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		input, err := parseArtifactContent(args[2:])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result artifactingest.Result
		err = session.DoJSON(ctx, http.MethodPost, "artifacts/"+url.PathEscape(base.ArtifactID)+"/revisions", input, &result,
			clientHeader("Idempotency-Key", newIdempotencyKey()), clientHeader("If-Match", fmt.Sprintf(`"%d"`, base.Version)))
		if err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeArtifactResult(result, fmt.Sprintf("Revised %s to version %d.", result.Artifact.ArtifactID, result.Artifact.Version), false, jsonOutput, stdout, stderr, command)
	case "attach":
		if len(args) != 4 || args[2] != "--to" {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected artifact attach <artifact-id>@<version> --to <kind>:<id>"))
		}
		reference, err := parseArtifactReference(args[1])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		target, err := parseArtifactTarget(args[3])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result artifactbinding.Version
		err = session.DoJSON(ctx, http.MethodPost, "artifact-bindings", artifactops.AttachInput{Artifact: reference, Target: target}, &result, clientHeader("Idempotency-Key", newIdempotencyKey()))
		if err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeArtifactResult(result, "Attached as "+result.BindingID+".", false, jsonOutput, stdout, stderr, command)
	case "detach":
		if len(args) != 2 || !strings.HasPrefix(args[1], "binding_") {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected artifact detach <binding-id>"))
		}
		var result artifactbinding.Version
		err := session.DoJSON(ctx, http.MethodDelete, "artifact-bindings/"+url.PathEscape(args[1]), nil, &result, clientHeader("Idempotency-Key", newIdempotencyKey()))
		if err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeArtifactResult(result, "Detached "+result.BindingID+".", false, jsonOutput, stdout, stderr, command)
	case "list":
		endpoint := "artifacts"
		if len(args) == 3 && args[1] == "--target" {
			target, err := parseArtifactTarget(args[2])
			if err != nil {
				return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
			}
			endpoint += "?targetKind=" + url.QueryEscape(string(target.Kind)) + "&targetId=" + url.QueryEscape(target.ID)
		} else if len(args) != 1 {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected artifact list [--target <kind>:<id>]"))
		}
		var result []artifactops.ArtifactView
		if err := session.DoJSON(ctx, http.MethodGet, endpoint, nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var human strings.Builder
		for _, value := range result {
			_, _ = fmt.Fprintf(&human, "%s@%d %s %s\n", value.Artifact.ArtifactID, value.Artifact.Version, value.Artifact.DetectedMediaType, value.Freshness)
		}
		return writeArtifactResult(result, strings.TrimSuffix(human.String(), "\n"), false, jsonOutput, stdout, stderr, command)
	case "show", "extract", "lint", "representations":
		if len(args) != 2 {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected artifact "+args[0]+" <artifact-id>@<version>"))
		}
		reference, err := parseArtifactReference(args[1])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		return runArtifactReferenceCommand(ctx, session, args[0], reference, jsonOutput, stdout, stderr, command)
	case "diff":
		input, err := parseArtifactDiff(args[1:])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		var result artifactops.VersionDiff
		query := url.Values{"from": {strconv.FormatUint(input.From, 10)}, "to": {strconv.FormatUint(input.To, 10)}}
		if input.FromRepresentationID != "" {
			query.Set("fromRepresentationId", input.FromRepresentationID)
		}
		if input.ToRepresentationID != "" {
			query.Set("toRepresentationId", input.ToRepresentationID)
		}
		if input.Cursor != "" {
			query.Set("cursor", input.Cursor)
		}
		if input.Limit != 0 {
			query.Set("limit", strconv.Itoa(input.Limit))
		}
		endpoint := fmt.Sprintf("artifacts/%s/diff?%s", url.PathEscape(input.ArtifactID), query.Encode())
		if err := session.DoJSON(ctx, http.MethodGet, endpoint, nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		human := fmt.Sprintf("Compared %s versions %d..%d: text diff %s", result.ArtifactID, result.From, result.To, result.TextDiff.Status)
		if result.TextDiff.Reason != "" {
			human += " (" + result.TextDiff.Reason + ")"
		}
		return writeArtifactResult(result, human+".", false, jsonOutput, stdout, stderr, command)
	case "impact":
		if len(args) != 4 && len(args) != 6 {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("expected artifact impact <artifact-id>@<version> --target <kind>:<id> [--run <run-id>]"))
		}
		reference, err := parseArtifactReference(args[1])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		if args[2] != "--target" {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("--target is required"))
		}
		target, err := parseArtifactTarget(args[3])
		if err != nil {
			return artifactArgumentError(stdout, stderr, jsonOutput, command, err)
		}
		runID := ""
		if len(args) == 6 {
			if args[4] != "--run" || args[5] == "" {
				return artifactArgumentError(stdout, stderr, jsonOutput, command, errors.New("--run requires a run ID"))
			}
			runID = args[5]
		}
		var result impactassessment.Assessment
		body := struct {
			Target artifactbinding.Target `json:"target"`
			RunID  string                 `json:"runId,omitempty"`
		}{target, runID}
		endpoint := fmt.Sprintf("artifacts/%s/impact?version=%d", url.PathEscape(reference.ArtifactID), reference.Version)
		if err := session.DoJSON(ctx, http.MethodPost, endpoint, body, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		actions := make([]string, len(result.Proposals))
		for index, proposal := range result.Proposals {
			actions[index] = string(proposal.Action())
		}
		return writeArtifactResult(result, "Impact proposals: "+strings.Join(actions, ", ")+".", false, jsonOutput, stdout, stderr, command)
	default:
		return artifactArgumentError(stdout, stderr, jsonOutput, "darkstar artifact", fmt.Errorf("unknown artifact command %q", args[0]))
	}
}

func parseArtifactDiff(args []string) (artifactops.DiffInput, error) {
	var input artifactops.DiffInput
	if len(args) < 1 || !strings.HasPrefix(args[0], "artifact_") {
		return input, errors.New("expected artifact diff <artifact-id> --from <version> --to <version>")
	}
	input.ArtifactID = args[0]
	seen := map[string]bool{}
	for index := 1; index < len(args); index += 2 {
		if index+1 >= len(args) || seen[args[index]] {
			return input, errors.New("diff options must be unique name/value pairs")
		}
		seen[args[index]] = true
		value := args[index+1]
		if value == "" {
			return input, fmt.Errorf("%s requires a value", args[index])
		}
		switch args[index] {
		case "--from":
			parsed, err := positiveVersion(value)
			if err != nil {
				return input, err
			}
			input.From = parsed
		case "--to":
			parsed, err := positiveVersion(value)
			if err != nil {
				return input, err
			}
			input.To = parsed
		case "--from-representation":
			input.FromRepresentationID = value
		case "--to-representation":
			input.ToRepresentationID = value
		case "--cursor":
			input.Cursor = value
		case "--limit":
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 200 {
				return input, errors.New("--limit must be between 1 and 200")
			}
			input.Limit = parsed
		default:
			return input, fmt.Errorf("unknown diff option %q", args[index])
		}
	}
	if input.From == 0 || input.To == 0 {
		return input, errors.New("--from and --to are required")
	}
	if (input.FromRepresentationID != "" && !strings.HasPrefix(input.FromRepresentationID, "representation_")) || (input.ToRepresentationID != "" && !strings.HasPrefix(input.ToRepresentationID, "representation_")) {
		return input, errors.New("representation IDs must start with representation_")
	}
	return input, nil
}

func runArtifactReferenceCommand(ctx context.Context, session *clientapi.Session, verb string, reference artifactregistry.VersionRef, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	base := "artifacts/" + url.PathEscape(reference.ArtifactID)
	switch verb {
	case "show":
		var result artifactops.ArtifactView
		if err := session.DoJSON(ctx, http.MethodGet, fmt.Sprintf("%s?version=%d", base, reference.Version), nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeArtifactResult(result, fmt.Sprintf("%s@%d %s %s.", result.Artifact.ArtifactID, result.Artifact.Version, result.Artifact.DetectedMediaType, result.Freshness), false, jsonOutput, stdout, stderr, command)
	case "extract":
		var result artifactderive.Result
		if err := session.DoJSON(ctx, http.MethodPost, fmt.Sprintf("%s/extract?version=%d", base, reference.Version), nil, &result, clientHeader("Idempotency-Key", newIdempotencyKey())); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeArtifactResult(result, fmt.Sprintf("Extracted %d representation(s).", len(result.Representations)), false, jsonOutput, stdout, stderr, command)
	case "lint":
		var result artifactops.LintResult
		if err := session.DoJSON(ctx, http.MethodGet, fmt.Sprintf("%s/lint?version=%d", base, reference.Version), nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		human := "Artifact lint passed."
		if !result.Valid {
			human = fmt.Sprintf("Artifact lint found %d issue(s).", len(result.Issues))
		}
		return writeArtifactResult(result, human, !result.Valid, jsonOutput, stdout, stderr, command)
	case "representations":
		var result []representationregistry.Representation
		if err := session.DoJSON(ctx, http.MethodGet, fmt.Sprintf("%s/representations?version=%d", base, reference.Version), nil, &result); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeArtifactResult(result, fmt.Sprintf("%d representation(s).", len(result)), false, jsonOutput, stdout, stderr, command)
	default:
		panic("validated artifact reference command was not handled")
	}
}

func parseArtifactContent(args []string) (artifactops.IngestInput, error) {
	var input artifactops.IngestInput
	var sources int
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--file":
			if index+1 >= len(args) {
				return input, errors.New("--file requires a path")
			}
			index++
			content, err := os.ReadFile(args[index])
			if err != nil {
				return input, err
			}
			if len(content) > maxCLIArtifactBytes {
				return input, errors.New("artifact exceeds 25 MiB CLI limit")
			}
			input.SourceKind, input.SourceName, input.Content = artifactregistry.SourceFile, filepath.Base(args[index]), content
			sources++
			if input.MediaType == "" {
				input.MediaType = mime.TypeByExtension(strings.ToLower(filepath.Ext(args[index])))
			}
		case "--paste":
			if index+1 >= len(args) {
				return input, errors.New("--paste requires text")
			}
			index++
			input.SourceKind, input.SourceName, input.Content = artifactregistry.SourcePaste, "pasted-note.txt", []byte(args[index])
			sources++
		case "--stdin":
			content, err := io.ReadAll(io.LimitReader(os.Stdin, maxCLIArtifactBytes+1))
			if err != nil {
				return input, err
			}
			if len(content) > maxCLIArtifactBytes {
				return input, errors.New("artifact exceeds 25 MiB CLI limit")
			}
			input.SourceKind, input.SourceName, input.Content = artifactregistry.SourceStdin, "stdin.txt", content
			sources++
		case "--media-type":
			if index+1 >= len(args) {
				return input, errors.New("--media-type requires a value")
			}
			index++
			input.MediaType = args[index]
		case "--role":
			if index+1 >= len(args) {
				return input, errors.New("--role requires a value")
			}
			index++
			input.Roles = append(input.Roles, args[index])
		case "--tag":
			if index+1 >= len(args) {
				return input, errors.New("--tag requires a value")
			}
			index++
			input.Tags = append(input.Tags, args[index])
		case "--sensitivity":
			if index+1 >= len(args) {
				return input, errors.New("--sensitivity requires a value")
			}
			index++
			input.Sensitivity = artifactregistry.Sensitivity(args[index])
			switch input.Sensitivity {
			case artifactregistry.SensitivityUnknown, artifactregistry.SensitivityPublic, artifactregistry.SensitivityInternal, artifactregistry.SensitivitySensitive, artifactregistry.SensitivitySecret:
			default:
				return input, fmt.Errorf("unsupported sensitivity %q", input.Sensitivity)
			}
		default:
			return input, fmt.Errorf("unknown ingestion option %q", args[index])
		}
	}
	if sources != 1 {
		return input, errors.New("exactly one of --file, --paste, or --stdin is required")
	}
	if input.MediaType == "" {
		if input.SourceKind == artifactregistry.SourceFile {
			input.MediaType = "application/octet-stream"
		} else {
			input.MediaType = "text/plain"
		}
	}
	return input, nil
}

func parseArtifactReference(value string) (artifactregistry.VersionRef, error) {
	id, versionText, found := strings.Cut(value, "@")
	if !found || !strings.HasPrefix(id, "artifact_") {
		return artifactregistry.VersionRef{}, errors.New("artifact reference must be <artifact-id>@<version>")
	}
	version, err := positiveVersion(versionText)
	if err != nil {
		return artifactregistry.VersionRef{}, err
	}
	return artifactregistry.VersionRef{ArtifactID: id, Version: version}, nil
}

func parseArtifactTarget(value string) (artifactbinding.Target, error) {
	kind, id, found := strings.Cut(value, ":")
	target := artifactbinding.Target{Kind: artifactbinding.TargetKind(kind), ID: id}
	if !found || id == "" {
		return target, errors.New("target must be <kind>:<id>")
	}
	switch target.Kind {
	case artifactbinding.TargetProject, artifactbinding.TargetWork, artifactbinding.TargetRun, artifactbinding.TargetNode, artifactbinding.TargetCheckpoint, artifactbinding.TargetDecision, artifactbinding.TargetStory, artifactbinding.TargetImplementationPoint:
		return target, nil
	default:
		return target, fmt.Errorf("unsupported target kind %q", kind)
	}
}

func positiveVersion(value string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("artifact version must be positive")
	}
	return parsed, nil
}

func clientHeader(name, value string) clientapi.RequestOption {
	return clientapi.WithHeader(name, value)
}

func artifactArgumentError(stdout, stderr io.Writer, jsonOutput bool, command string, err error) int {
	return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
}

func writeArtifactResult(result any, human string, validationFailed, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, artifactMachineOutput{SchemaVersion: machineSchemaVersion, Result: result}); err != nil {
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
