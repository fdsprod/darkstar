package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	clientapi "darkstar/src/api/client"
	"darkstar/src/core/readinesscontrol"
	"darkstar/src/core/routeassessment"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

var (
	readinessTokenPattern  = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	readinessDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type readinessJSONClient interface {
	DoJSON(context.Context, string, string, any, any, ...clientapi.RequestOption) error
}

type readinessCommand struct {
	operation      string
	runID          string
	action         routeassessment.Choice
	reason         string
	remedyCode     string
	idempotencyKey string
}

type readinessDecisionBody struct {
	Action           routeassessment.Choice `json:"action"`
	AssessmentDigest string                 `json:"assessmentDigest"`
	Reason           string                 `json:"reason"`
	RemedyCode       string                 `json:"remedyCode,omitempty"`
}

// readinessViewResponse reconstructs the closed finding union at the transport
// boundary. The core view remains the presentation model after decoding.
type readinessViewResponse readinesscontrol.View

func (response *readinessViewResponse) UnmarshalJSON(content []byte) error {
	var wire struct {
		Assessment struct {
			AssessmentID string                           `json:"assessmentId"`
			RunID        string                           `json:"runId"`
			NodeID       workflow.Identifier              `json:"nodeId"`
			Scores       []routeassessment.Score          `json:"scores"`
			Findings     []json.RawMessage                `json:"findings"`
			Remedies     []workflow.ReadinessRemedy       `json:"remedies"`
			Disposition  routeassessment.Disposition      `json:"disposition"`
			Digest       string                           `json:"digest"`
			RouteChange  *routeassessment.RouteChangeView `json:"routeChange"`
		} `json:"assessment"`
		Status          statestore.ReadinessAssessmentStatus    `json:"status"`
		AllowedActions  []readinesscontrol.AllowedAction        `json:"allowedActions"`
		Decision        *statestore.ReadinessDecisionProjection `json:"decision"`
		ResourceVersion uint64                                  `json:"resourceVersion"`
		CreatedAt       time.Time                               `json:"createdAt"`
		UpdatedAt       time.Time                               `json:"updatedAt"`
	}
	if err := json.Unmarshal(content, &wire); err != nil {
		return err
	}
	findings := make([]routeassessment.Finding, 0, len(wire.Assessment.Findings))
	for index, raw := range wire.Assessment.Findings {
		finding, err := decodeReadinessFinding(raw)
		if err != nil {
			return fmt.Errorf("decode readiness finding %d: %w", index, err)
		}
		findings = append(findings, finding)
	}
	*response = readinessViewResponse(readinesscontrol.View{
		Assessment: routeassessment.View{
			AssessmentID: wire.Assessment.AssessmentID, RunID: wire.Assessment.RunID, NodeID: wire.Assessment.NodeID,
			Scores: wire.Assessment.Scores, Findings: findings, Remedies: wire.Assessment.Remedies,
			Disposition: wire.Assessment.Disposition, Digest: wire.Assessment.Digest, RouteChange: wire.Assessment.RouteChange,
		},
		Status: wire.Status, AllowedActions: wire.AllowedActions, Decision: wire.Decision,
		ResourceVersion: wire.ResourceVersion, CreatedAt: wire.CreatedAt, UpdatedAt: wire.UpdatedAt,
	})
	return nil
}

func decodeReadinessFinding(content json.RawMessage) (routeassessment.Finding, error) {
	var discriminator struct {
		Level routeassessment.Level `json:"level"`
	}
	if err := json.Unmarshal(content, &discriminator); err != nil {
		return nil, err
	}
	var finding routeassessment.Finding
	switch discriminator.Level {
	case routeassessment.LevelInformation:
		finding = &routeassessment.InformationFinding{}
	case routeassessment.LevelRecommendation:
		finding = &routeassessment.RecommendationFinding{}
	case routeassessment.LevelPolicyGate:
		finding = &routeassessment.PolicyGateFinding{}
	case routeassessment.LevelInvariant:
		finding = &routeassessment.InvariantFinding{}
	default:
		return nil, fmt.Errorf("unsupported level %q", discriminator.Level)
	}
	if err := json.Unmarshal(content, finding); err != nil {
		return nil, err
	}
	return finding, nil
}

func runReadiness(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	request, err := parseRunReadiness(args)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar run readiness", "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
	}
	session, code := connectRunSession("darkstar run readiness "+request.operation, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	return executeRunReadiness(context.Background(), session, request, jsonOutput, stdout, stderr)
}

func parseRunReadiness(args []string) (readinessCommand, error) {
	if len(args) < 2 || (args[0] != "show" && args[0] != "decide") {
		return readinessCommand{}, errors.New("expected run readiness show|decide <run-id>")
	}
	if !runIdentityPattern.MatchString(args[1]) {
		return readinessCommand{}, fmt.Errorf("run readiness %s requires a canonical run_ ULID", args[0])
	}
	result := readinessCommand{operation: args[0], runID: args[1]}
	if args[0] == "show" {
		if len(args) != 2 {
			return readinessCommand{}, errors.New("expected run readiness show <run-id>")
		}
		return result, nil
	}

	seenAction, seenReason, seenRemedy, seenKey := false, false, false, false
	for index := 2; index < len(args); index += 2 {
		if index+1 >= len(args) || args[index+1] == "" {
			return readinessCommand{}, fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--action":
			if seenAction {
				return readinessCommand{}, errors.New("--action may be specified only once")
			}
			seenAction, result.action = true, routeassessment.Choice(value)
		case "--reason":
			if seenReason {
				return readinessCommand{}, errors.New("--reason may be specified only once")
			}
			seenReason, result.reason = true, value
		case "--remedy":
			if seenRemedy {
				return readinessCommand{}, errors.New("--remedy may be specified only once")
			}
			seenRemedy, result.remedyCode = true, value
		case "--idempotency-key":
			if seenKey {
				return readinessCommand{}, errors.New("--idempotency-key may be specified only once")
			}
			seenKey, result.idempotencyKey = true, value
		default:
			return readinessCommand{}, fmt.Errorf("unknown run readiness decide option %q", args[index])
		}
	}
	if !seenAction || !validReadinessChoice(result.action) {
		return readinessCommand{}, errors.New("--action must be one of continue, accept_route_change, supply_input, or cancel")
	}
	if !seenReason || strings.TrimSpace(result.reason) == "" || result.reason != strings.TrimSpace(result.reason) || len(result.reason) > 4096 {
		return readinessCommand{}, errors.New("--reason is required without surrounding whitespace and may contain at most 4096 bytes")
	}
	if result.action == routeassessment.ChoiceSupplyInput {
		if !readinessTokenPattern.MatchString(result.remedyCode) {
			return readinessCommand{}, errors.New("supply_input requires --remedy with one canonical remedy code")
		}
	} else if seenRemedy {
		return readinessCommand{}, fmt.Errorf("--remedy is not valid for %s", result.action)
	}
	if result.idempotencyKey == "" {
		result.idempotencyKey = newIdempotencyKey()
	} else if strings.TrimSpace(result.idempotencyKey) != result.idempotencyKey || len(result.idempotencyKey) < 8 || len(result.idempotencyKey) > 128 {
		return readinessCommand{}, errors.New("--idempotency-key must be between 8 and 128 bytes without surrounding whitespace")
	}
	return result, nil
}

func validReadinessChoice(choice routeassessment.Choice) bool {
	switch choice {
	case routeassessment.ChoiceContinue, routeassessment.ChoiceAcceptRouteChange, routeassessment.ChoiceSupplyInput, routeassessment.ChoiceCancel:
		return true
	default:
		return false
	}
}

func executeRunReadiness(ctx context.Context, client readinessJSONClient, command readinessCommand, jsonOutput bool, stdout, stderr io.Writer) int {
	commandName := "darkstar run readiness " + command.operation
	resource := "runs/" + command.runID + "/readiness"
	var currentResponse readinessViewResponse
	if err := client.DoJSON(ctx, http.MethodGet, resource, nil, &currentResponse); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, commandName, err)
	}
	current := readinesscontrol.View(currentResponse)
	if command.operation == "show" {
		return writeReadinessView(current, jsonOutput, stdout, stderr, commandName)
	}
	if current.ResourceVersion == 0 || !readinessDigestPattern.MatchString(current.Assessment.Digest) {
		return writeCommandError(stdout, stderr, jsonOutput, commandName, "INTERNAL_INVARIANT_VIOLATION", "the daemon returned an incomplete readiness concurrency boundary", false, ExitInvariantViolation)
	}
	body := readinessDecisionBody{
		Action: command.action, AssessmentDigest: current.Assessment.Digest,
		Reason: command.reason, RemedyCode: command.remedyCode,
	}
	var decidedResponse readinessViewResponse
	if err := client.DoJSON(ctx, http.MethodPost, resource+"/decisions", body, &decidedResponse,
		clientapi.WithHeader("Idempotency-Key", command.idempotencyKey),
		clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, current.ResourceVersion))); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, commandName, err)
	}
	return writeReadinessView(readinesscontrol.View(decidedResponse), jsonOutput, stdout, stderr, commandName)
}

func writeReadinessView(view readinesscontrol.View, jsonOutput bool, stdout, stderr io.Writer, command string) int {
	if jsonOutput {
		if err := writeJSON(stdout, runMachineOutput{SchemaVersion: machineSchemaVersion, Result: view}); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
		return int(ExitSuccess)
	}
	assessment := view.Assessment
	_, _ = fmt.Fprintf(stdout, "Readiness %s for %s: %s (%s).\n", assessment.AssessmentID, assessment.RunID, assessment.Disposition, view.Status)
	_, _ = fmt.Fprintf(stdout, "Node: %s\nDigest: %s\n", assessment.NodeID, assessment.Digest)
	if len(assessment.Scores) != 0 {
		_, _ = fmt.Fprintln(stdout, "Scores:")
		for _, score := range assessment.Scores {
			_, _ = fmt.Fprintf(stdout, "- %s: %.2f\n", score.Name, score.Value)
		}
	}
	if len(assessment.Findings) != 0 {
		_, _ = fmt.Fprintln(stdout, "Findings:")
		for _, finding := range assessment.Findings {
			code, summary := readinessFindingText(finding)
			_, _ = fmt.Fprintf(stdout, "- [%s] %s: %s\n", finding.Level(), code, summary)
		}
	}
	if assessment.RouteChange != nil {
		impact := assessment.RouteChange.Impact
		_, _ = fmt.Fprintf(stdout, "Route change: %s (%s)\n", assessment.RouteChange.PatchID, assessment.RouteChange.AuthorizationMode)
		_, _ = fmt.Fprintf(stdout, "Impact: +%d/-%d nodes, +%d/-%d transitions\n",
			len(impact.AddedNodes), len(impact.RemovedNodes), len(impact.AddedTransitions), len(impact.RemovedTransitions))
	}
	if len(view.AllowedActions) != 0 {
		_, _ = fmt.Fprintln(stdout, "Allowed actions:")
		for _, action := range view.AllowedActions {
			if action.Remedy == nil {
				_, _ = fmt.Fprintf(stdout, "- %s\n", action.Choice)
			} else {
				_, _ = fmt.Fprintf(stdout, "- %s --remedy %s (%s)\n", action.Choice, action.Remedy.Code, action.Remedy.Description)
			}
		}
	}
	if view.Decision != nil {
		_, _ = fmt.Fprintf(stdout, "Decision: %s (%s; effect %s)\n", view.Decision.Choice, view.Decision.Reason, view.Decision.EffectStatus)
	}
	return int(ExitSuccess)
}

func readinessFindingText(finding routeassessment.Finding) (string, string) {
	switch value := finding.(type) {
	case routeassessment.InformationFinding:
		return value.Code, value.Summary
	case *routeassessment.InformationFinding:
		return value.Code, value.Summary
	case routeassessment.RecommendationFinding:
		return value.Code, value.Summary
	case *routeassessment.RecommendationFinding:
		return value.Code, value.Summary
	case routeassessment.PolicyGateFinding:
		return value.Code, value.Summary
	case *routeassessment.PolicyGateFinding:
		return value.Code, value.Summary
	case routeassessment.InvariantFinding:
		return value.Code, value.Summary
	case *routeassessment.InvariantFinding:
		return value.Code, value.Summary
	default:
		return "unknown", "Unsupported readiness finding"
	}
}
