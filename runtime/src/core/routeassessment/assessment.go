// Package routeassessment validates semantic route/readiness output and keeps
// it separate from executable workflow state.
package routeassessment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

const (
	Kind          = "route_readiness_assessment"
	SchemaVersion = 1
)

type ErrorCode string

const (
	ErrorInvalid          ErrorCode = "ASSESSMENT_INVALID"
	ErrorContractMismatch ErrorCode = "ASSESSMENT_CONTRACT_MISMATCH"
	ErrorRouteInvalid     ErrorCode = "ASSESSMENT_ROUTE_INVALID"
	ErrorDecisionInvalid  ErrorCode = "ASSESSMENT_DECISION_INVALID"
	ErrorDecisionStore    ErrorCode = "ASSESSMENT_DECISION_STORE_FAILED"
)

type Error struct {
	Code     ErrorCode
	Message  string
	Location string
	Cause    error
}

func (failure *Error) Error() string {
	if failure.Location == "" {
		return fmt.Sprintf("%s: %s", failure.Code, failure.Message)
	}
	return fmt.Sprintf("%s at %s: %s", failure.Code, failure.Location, failure.Message)
}

func (failure *Error) Unwrap() error { return failure.Cause }

type Evidence struct {
	Source      string `json:"source"`
	Observation string `json:"observation"`
}

type Score struct {
	Name     string     `json:"name"`
	Value    float64    `json:"value"`
	Evidence []Evidence `json:"evidence"`
}

type Level string

const (
	LevelInformation    Level = "information"
	LevelRecommendation Level = "recommendation"
	LevelPolicyGate     Level = "policy_gate"
	LevelInvariant      Level = "invariant"
)

// Finding is a closed union. Policy and invariant states only exist on their
// corresponding variants, preventing contradictory combinations such as an
// informational finding that claims a gate is unsatisfied.
type Finding interface {
	Level() Level
	findingCode() string
	findingEvidence() []Evidence
	isFinding()
}

type InformationFinding struct {
	Code     string     `json:"code"`
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence"`
}

func (InformationFinding) Level() Level                        { return LevelInformation }
func (finding InformationFinding) findingCode() string         { return finding.Code }
func (finding InformationFinding) findingEvidence() []Evidence { return finding.Evidence }
func (InformationFinding) isFinding()                          {}
func (finding InformationFinding) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Level    Level      `json:"level"`
		Code     string     `json:"code"`
		Summary  string     `json:"summary"`
		Evidence []Evidence `json:"evidence"`
	}{LevelInformation, finding.Code, finding.Summary, finding.Evidence})
}

func (finding *InformationFinding) UnmarshalJSON(content []byte) error {
	var wire struct {
		Level    Level      `json:"level"`
		Code     string     `json:"code"`
		Summary  string     `json:"summary"`
		Evidence []Evidence `json:"evidence"`
	}
	if err := strictDecode(content, &wire); err != nil {
		return err
	}
	if wire.Level != LevelInformation {
		return fmt.Errorf("information finding has level %q", wire.Level)
	}
	*finding = InformationFinding{Code: wire.Code, Summary: wire.Summary, Evidence: wire.Evidence}
	return nil
}

type RecommendationFinding struct {
	Code       string     `json:"code"`
	Summary    string     `json:"summary"`
	Evidence   []Evidence `json:"evidence"`
	RemedyCode string     `json:"remedyCode"`
}

func (RecommendationFinding) Level() Level                        { return LevelRecommendation }
func (finding RecommendationFinding) findingCode() string         { return finding.Code }
func (finding RecommendationFinding) findingEvidence() []Evidence { return finding.Evidence }
func (RecommendationFinding) isFinding()                          {}
func (finding RecommendationFinding) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Level      Level      `json:"level"`
		Code       string     `json:"code"`
		Summary    string     `json:"summary"`
		Evidence   []Evidence `json:"evidence"`
		RemedyCode string     `json:"remedyCode"`
	}{LevelRecommendation, finding.Code, finding.Summary, finding.Evidence, finding.RemedyCode})
}

func (finding *RecommendationFinding) UnmarshalJSON(content []byte) error {
	var wire struct {
		Level      Level      `json:"level"`
		Code       string     `json:"code"`
		Summary    string     `json:"summary"`
		Evidence   []Evidence `json:"evidence"`
		RemedyCode string     `json:"remedyCode"`
	}
	if err := strictDecode(content, &wire); err != nil {
		return err
	}
	if wire.Level != LevelRecommendation {
		return fmt.Errorf("recommendation finding has level %q", wire.Level)
	}
	*finding = RecommendationFinding{Code: wire.Code, Summary: wire.Summary, Evidence: wire.Evidence, RemedyCode: wire.RemedyCode}
	return nil
}

type GateStatus string

const (
	GateSatisfied   GateStatus = "satisfied"
	GateUnsatisfied GateStatus = "unsatisfied"
)

type PolicyGateFinding struct {
	Code       string              `json:"code"`
	Summary    string              `json:"summary"`
	Evidence   []Evidence          `json:"evidence"`
	Policy     workflow.Identifier `json:"policy"`
	Status     GateStatus          `json:"status"`
	RemedyCode string              `json:"remedyCode,omitempty"`
}

func (PolicyGateFinding) Level() Level                        { return LevelPolicyGate }
func (finding PolicyGateFinding) findingCode() string         { return finding.Code }
func (finding PolicyGateFinding) findingEvidence() []Evidence { return finding.Evidence }
func (PolicyGateFinding) isFinding()                          {}
func (finding PolicyGateFinding) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Level      Level               `json:"level"`
		Code       string              `json:"code"`
		Summary    string              `json:"summary"`
		Evidence   []Evidence          `json:"evidence"`
		Policy     workflow.Identifier `json:"policy"`
		Status     GateStatus          `json:"status"`
		RemedyCode string              `json:"remedyCode,omitempty"`
	}{LevelPolicyGate, finding.Code, finding.Summary, finding.Evidence, finding.Policy, finding.Status, finding.RemedyCode})
}

func (finding *PolicyGateFinding) UnmarshalJSON(content []byte) error {
	var wire struct {
		Level      Level               `json:"level"`
		Code       string              `json:"code"`
		Summary    string              `json:"summary"`
		Evidence   []Evidence          `json:"evidence"`
		Policy     workflow.Identifier `json:"policy"`
		Status     GateStatus          `json:"status"`
		RemedyCode string              `json:"remedyCode"`
	}
	if err := strictDecode(content, &wire); err != nil {
		return err
	}
	if wire.Level != LevelPolicyGate {
		return fmt.Errorf("policy-gate finding has level %q", wire.Level)
	}
	*finding = PolicyGateFinding{Code: wire.Code, Summary: wire.Summary, Evidence: wire.Evidence, Policy: wire.Policy, Status: wire.Status, RemedyCode: wire.RemedyCode}
	return nil
}

type InvariantStatus string

const (
	InvariantUpheld   InvariantStatus = "upheld"
	InvariantViolated InvariantStatus = "violated"
)

type InvariantFinding struct {
	Code       string          `json:"code"`
	Summary    string          `json:"summary"`
	Evidence   []Evidence      `json:"evidence"`
	Invariant  string          `json:"invariant"`
	Status     InvariantStatus `json:"status"`
	RemedyCode string          `json:"remedyCode,omitempty"`
}

func (InvariantFinding) Level() Level                        { return LevelInvariant }
func (finding InvariantFinding) findingCode() string         { return finding.Code }
func (finding InvariantFinding) findingEvidence() []Evidence { return finding.Evidence }
func (InvariantFinding) isFinding()                          {}
func (finding InvariantFinding) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Level      Level           `json:"level"`
		Code       string          `json:"code"`
		Summary    string          `json:"summary"`
		Evidence   []Evidence      `json:"evidence"`
		Invariant  string          `json:"invariant"`
		Status     InvariantStatus `json:"status"`
		RemedyCode string          `json:"remedyCode,omitempty"`
	}{LevelInvariant, finding.Code, finding.Summary, finding.Evidence, finding.Invariant, finding.Status, finding.RemedyCode})
}

func (finding *InvariantFinding) UnmarshalJSON(content []byte) error {
	var wire struct {
		Level      Level           `json:"level"`
		Code       string          `json:"code"`
		Summary    string          `json:"summary"`
		Evidence   []Evidence      `json:"evidence"`
		Invariant  string          `json:"invariant"`
		Status     InvariantStatus `json:"status"`
		RemedyCode string          `json:"remedyCode"`
	}
	if err := strictDecode(content, &wire); err != nil {
		return err
	}
	if wire.Level != LevelInvariant {
		return fmt.Errorf("invariant finding has level %q", wire.Level)
	}
	*finding = InvariantFinding{Code: wire.Code, Summary: wire.Summary, Evidence: wire.Evidence, Invariant: wire.Invariant, Status: wire.Status, RemedyCode: wire.RemedyCode}
	return nil
}

// Submission is provider-produced evidence. ProposedPatch remains data only;
// Assess validates it but does not expose it for application.
type Submission struct {
	AssessmentID  string               `json:"assessmentId"`
	RunID         string               `json:"runId"`
	NodeID        workflow.Identifier  `json:"nodeId"`
	Scores        []Score              `json:"scores"`
	Findings      []Finding            `json:"findings"`
	ProposedPatch *workflow.RoutePatch `json:"proposedPatch,omitempty"`
}

func (submission Submission) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind          string               `json:"kind"`
		SchemaVersion int                  `json:"schemaVersion"`
		AssessmentID  string               `json:"assessmentId"`
		RunID         string               `json:"runId"`
		NodeID        workflow.Identifier  `json:"nodeId"`
		Scores        []Score              `json:"scores"`
		Findings      []Finding            `json:"findings"`
		ProposedPatch *workflow.RoutePatch `json:"proposedPatch,omitempty"`
	}{Kind, SchemaVersion, submission.AssessmentID, submission.RunID, submission.NodeID, submission.Scores, submission.Findings, submission.ProposedPatch})
}

func (submission *Submission) UnmarshalJSON(content []byte) error {
	var wire struct {
		Kind          string               `json:"kind"`
		SchemaVersion int                  `json:"schemaVersion"`
		AssessmentID  string               `json:"assessmentId"`
		RunID         string               `json:"runId"`
		NodeID        workflow.Identifier  `json:"nodeId"`
		Scores        []Score              `json:"scores"`
		Findings      []json.RawMessage    `json:"findings"`
		ProposedPatch *workflow.RoutePatch `json:"proposedPatch"`
	}
	if err := strictDecode(content, &wire); err != nil {
		return err
	}
	if wire.Kind != Kind || wire.SchemaVersion != SchemaVersion {
		return errors.New("unsupported route/readiness assessment boundary")
	}
	findings := make([]Finding, 0, len(wire.Findings))
	for index, raw := range wire.Findings {
		var discriminator struct {
			Level Level `json:"level"`
		}
		if err := json.Unmarshal(raw, &discriminator); err != nil {
			return fmt.Errorf("findings[%d]: %w", index, err)
		}
		var finding Finding
		switch discriminator.Level {
		case LevelInformation:
			finding = &InformationFinding{}
		case LevelRecommendation:
			finding = &RecommendationFinding{}
		case LevelPolicyGate:
			finding = &PolicyGateFinding{}
		case LevelInvariant:
			finding = &InvariantFinding{}
		default:
			return fmt.Errorf("findings[%d] has unsupported level %q", index, discriminator.Level)
		}
		if err := strictDecode(raw, finding); err != nil {
			return fmt.Errorf("findings[%d]: %w", index, err)
		}
		findings = append(findings, finding)
	}
	*submission = Submission{AssessmentID: wire.AssessmentID, RunID: wire.RunID, NodeID: wire.NodeID, Scores: wire.Scores, Findings: findings, ProposedPatch: wire.ProposedPatch}
	return nil
}

type Disposition string

const (
	DispositionReady            Disposition = "ready"
	DispositionChoiceRequired   Disposition = "choice_required"
	DispositionPolicyBlocked    Disposition = "policy_blocked"
	DispositionInvariantBlocked Disposition = "invariant_blocked"
)

type Snapshot struct {
	Submission  Submission  `json:"submission"`
	Disposition Disposition `json:"disposition"`
	Digest      string      `json:"digest"`
}

// Assessment hides the validated patch proposal so provider output can never
// be mistaken for executable workflow state.
type Assessment struct {
	submission  Submission
	disposition Disposition
	digest      string
	proposal    *workflow.RoutePatchProposal
}

func (assessment Assessment) Snapshot() Snapshot {
	return Snapshot{Submission: cloneSubmission(assessment.submission), Disposition: assessment.disposition, Digest: assessment.digest}
}

func (assessment Assessment) Disposition() Disposition { return assessment.disposition }

var tokenPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

// Assess validates semantic evidence against the authored node contract and
// validates any proposed route change through the ordinary route-patch path.
// Semantic proposals always require attributable approval, regardless of the
// project's automatic patch policy.
func Assess(document workflow.Document, current workflow.RouteState, context workflow.RouteContext, policyDigest string, submission Submission) (Assessment, error) {
	if err := validateSubmission(document, current, submission); err != nil {
		return Assessment{}, err
	}
	contract := document.Spec.Nodes[submission.NodeID].Fields().Readiness
	disposition := deriveDisposition(submission.Findings, contract)
	var proposal *workflow.RoutePatchProposal
	if submission.ProposedPatch != nil {
		if disposition == DispositionReady {
			return Assessment{}, failure(ErrorInvalid, "an informational or satisfied assessment cannot propose a route change", "/proposedPatch", nil)
		}
		if submission.ProposedPatch.Spec.ApprovalID != "" {
			return Assessment{}, failure(ErrorInvalid, "provider output cannot claim a pre-existing route approval", "/proposedPatch/spec/approvalId", nil)
		}
		validated, err := workflow.ProposeRoutePatch(document, current, *submission.ProposedPatch, context, workflow.RoutePatchPolicy{
			Mode: workflow.RoutePatchRequireApproval, PolicyDigest: policyDigest,
		})
		if err != nil {
			return Assessment{}, failure(ErrorRouteInvalid, "proposed route change failed deterministic validation", "/proposedPatch", err)
		}
		proposal = &validated
	}
	encoded, err := json.Marshal(submission)
	if err != nil {
		return Assessment{}, failure(ErrorInvalid, "encode normalized assessment", "/", err)
	}
	sum := sha256.Sum256(encoded)
	return Assessment{submission: cloneSubmission(submission), disposition: disposition, digest: hex.EncodeToString(sum[:]), proposal: proposal}, nil
}

type Choice string

const (
	ChoiceContinue          Choice = "continue"
	ChoiceAcceptRouteChange Choice = "accept_route_change"
	ChoiceCancel            Choice = "cancel"
)

type DecisionRequest struct {
	DecisionID string
	Choice     Choice
	Reason     string
	Actor      statestore.Actor
	DecidedAt  time.Time
}

type Decision struct {
	DecisionID       string           `json:"decisionId"`
	AssessmentID     string           `json:"assessmentId"`
	AssessmentDigest string           `json:"assessmentDigest"`
	RouteScopeDigest string           `json:"routeScopeDigest,omitempty"`
	Choice           Choice           `json:"choice"`
	Reason           string           `json:"reason"`
	Actor            statestore.Actor `json:"actor"`
	DecidedAt        time.Time        `json:"decidedAt"`
}

type DecisionRecorder interface {
	RecordRouteAssessmentDecision(context.Context, Decision) error
}

// Decide records the human choice before releasing a validated route proposal.
// A storage failure returns no proposal, so callers cannot accidentally mutate
// the route without durable decision evidence.
func (assessment Assessment) Decide(ctx context.Context, recorder DecisionRecorder, request DecisionRequest) (Decision, *workflow.RoutePatchProposal, error) {
	if recorder == nil || assessment.digest == "" {
		return Decision{}, nil, failure(ErrorDecisionInvalid, "validated assessment and decision recorder are required", "/decision", nil)
	}
	if err := validateDecisionRequest(assessment, request); err != nil {
		return Decision{}, nil, err
	}
	decision := Decision{
		DecisionID: request.DecisionID, AssessmentID: assessment.submission.AssessmentID, AssessmentDigest: assessment.digest,
		Choice: request.Choice, Reason: request.Reason, Actor: request.Actor, DecidedAt: request.DecidedAt.UTC(),
	}
	if request.Choice == ChoiceAcceptRouteChange {
		decision.RouteScopeDigest = assessment.proposal.ScopeDigest()
	}
	if err := recorder.RecordRouteAssessmentDecision(ctx, decision); err != nil {
		return Decision{}, nil, failure(ErrorDecisionStore, "record route/readiness choice", "/decision", err)
	}
	if request.Choice == ChoiceAcceptRouteChange {
		return decision, assessment.proposal, nil
	}
	return decision, nil, nil
}

func validateDecisionRequest(assessment Assessment, request DecisionRequest) error {
	if !tokenPattern.MatchString(request.DecisionID) {
		return failure(ErrorDecisionInvalid, "decisionId must be a canonical token", "/decision/decisionId", nil)
	}
	if strings.TrimSpace(request.Reason) == "" || request.Reason != strings.TrimSpace(request.Reason) {
		return failure(ErrorDecisionInvalid, "decision reason is required without surrounding whitespace", "/decision/reason", nil)
	}
	if request.Actor.Type != statestore.ActorUser && request.Actor.Type != statestore.ActorExternal {
		return failure(ErrorDecisionInvalid, "route/readiness choices require a user or attributable external actor", "/decision/actor", nil)
	}
	if strings.TrimSpace(request.Actor.ID) == "" || request.Actor.ID != strings.TrimSpace(request.Actor.ID) || request.DecidedAt.IsZero() {
		return failure(ErrorDecisionInvalid, "decision actor and timestamp are required", "/decision", nil)
	}
	switch request.Choice {
	case ChoiceCancel:
		return nil
	case ChoiceContinue:
		if assessment.disposition == DispositionPolicyBlocked || assessment.disposition == DispositionInvariantBlocked {
			return failure(ErrorDecisionInvalid, "an unsatisfied policy gate or violated invariant cannot be bypassed by continue", "/decision/choice", nil)
		}
		return nil
	case ChoiceAcceptRouteChange:
		if assessment.proposal == nil {
			return failure(ErrorDecisionInvalid, "assessment has no validated route change to accept", "/decision/choice", nil)
		}
		return nil
	default:
		return failure(ErrorDecisionInvalid, "decision choice is unsupported", "/decision/choice", nil)
	}
}

func validateSubmission(document workflow.Document, current workflow.RouteState, submission Submission) error {
	if !tokenPattern.MatchString(submission.AssessmentID) || strings.TrimSpace(submission.RunID) == "" || submission.RunID != current.RunID() {
		return failure(ErrorInvalid, "assessment identity and current run must match", "/", nil)
	}
	node, exists := document.Spec.Nodes[submission.NodeID]
	if !exists || !routeContains(current.Route(), submission.NodeID) {
		return failure(ErrorContractMismatch, "assessment node is absent from the current route", "/nodeId", nil)
	}
	contract := node.Fields().Readiness
	if contract == nil {
		return failure(ErrorContractMismatch, "assessment node has no readiness contract", "/nodeId", nil)
	}
	if len(submission.Scores) == 0 || len(submission.Findings) == 0 {
		return failure(ErrorInvalid, "assessment requires explicit scores and findings", "/", nil)
	}
	seenScores := map[string]bool{}
	for index, score := range submission.Scores {
		location := fmt.Sprintf("/scores/%d", index)
		if !tokenPattern.MatchString(score.Name) || seenScores[score.Name] || math.IsNaN(score.Value) || math.IsInf(score.Value, 0) || score.Value < 0 || score.Value > 1 {
			return failure(ErrorInvalid, "score name must be unique and its value must be between zero and one", location, nil)
		}
		seenScores[score.Name] = true
		if err := validateEvidence(score.Evidence, location+"/evidence"); err != nil {
			return err
		}
	}
	seenFindings := map[string]bool{}
	for index, finding := range submission.Findings {
		location := fmt.Sprintf("/findings/%d", index)
		code, findingEvidence, valid := findingIdentity(finding)
		if !valid || !tokenPattern.MatchString(code) || seenFindings[code] {
			return failure(ErrorInvalid, "finding code must be unique and canonical", location+"/code", nil)
		}
		seenFindings[code] = true
		if err := validateEvidence(findingEvidence, location+"/evidence"); err != nil {
			return err
		}
		switch value := finding.(type) {
		case InformationFinding:
			if err := validateSummary(value.Summary, location); err != nil {
				return err
			}
		case *InformationFinding:
			if err := validateSummary(value.Summary, location); err != nil {
				return err
			}
		case RecommendationFinding:
			if err := validateRecommendation(value.Summary, value.RemedyCode, contract, location); err != nil {
				return err
			}
		case *RecommendationFinding:
			if err := validateRecommendation(value.Summary, value.RemedyCode, contract, location); err != nil {
				return err
			}
		case PolicyGateFinding:
			if err := validatePolicyGate(value, contract, location); err != nil {
				return err
			}
		case *PolicyGateFinding:
			if err := validatePolicyGate(*value, contract, location); err != nil {
				return err
			}
		case InvariantFinding:
			if err := validateInvariant(value, contract, location); err != nil {
				return err
			}
		case *InvariantFinding:
			if err := validateInvariant(*value, contract, location); err != nil {
				return err
			}
		default:
			return failure(ErrorInvalid, fmt.Sprintf("unsupported finding type %T", finding), location, nil)
		}
	}
	return nil
}

func validateSummary(summary, location string) error {
	if strings.TrimSpace(summary) == "" || summary != strings.TrimSpace(summary) {
		return failure(ErrorInvalid, "finding summary is required without surrounding whitespace", location+"/summary", nil)
	}
	return nil
}

func validateRecommendation(summary, remedyCode string, contract *workflow.ReadinessContract, location string) error {
	if err := validateSummary(summary, location); err != nil {
		return err
	}
	if !hasRemedy(contract, remedyCode) {
		return failure(ErrorContractMismatch, "recommendation remedy is not declared by the node", location+"/remedyCode", nil)
	}
	return nil
}

func validatePolicyGate(finding PolicyGateFinding, contract *workflow.ReadinessContract, location string) error {
	if err := validateSummary(finding.Summary, location); err != nil {
		return err
	}
	if finding.Status != GateSatisfied && finding.Status != GateUnsatisfied {
		return failure(ErrorInvalid, "policy gate status is unsupported", location+"/status", nil)
	}
	found := false
	for _, gate := range contract.PolicyGates {
		if gate.Policy == finding.Policy {
			found = true
			break
		}
	}
	if !found {
		return failure(ErrorContractMismatch, "policy gate is not declared by the node", location+"/policy", nil)
	}
	if finding.Status == GateUnsatisfied && !hasRemedy(contract, finding.RemedyCode) {
		return failure(ErrorContractMismatch, "unsatisfied policy gate requires a declared remedy", location+"/remedyCode", nil)
	}
	if finding.Status == GateSatisfied && finding.RemedyCode != "" {
		return failure(ErrorInvalid, "a satisfied policy gate cannot prescribe a remedy", location+"/remedyCode", nil)
	}
	return nil
}

func validateInvariant(finding InvariantFinding, contract *workflow.ReadinessContract, location string) error {
	if err := validateSummary(finding.Summary, location); err != nil {
		return err
	}
	declared := false
	for _, invariant := range contract.Invariants {
		if invariant == finding.Invariant {
			declared = true
			break
		}
	}
	if !declared {
		return failure(ErrorContractMismatch, "invariant is not declared by the node", location+"/invariant", nil)
	}
	if finding.Status != InvariantUpheld && finding.Status != InvariantViolated {
		return failure(ErrorInvalid, "invariant status is unsupported", location+"/status", nil)
	}
	if finding.Status == InvariantViolated && !hasRemedy(contract, finding.RemedyCode) {
		return failure(ErrorContractMismatch, "violated invariant requires a declared remedy", location+"/remedyCode", nil)
	}
	if finding.Status == InvariantUpheld && finding.RemedyCode != "" {
		return failure(ErrorInvalid, "an upheld invariant cannot prescribe a remedy", location+"/remedyCode", nil)
	}
	return nil
}

func validateEvidence(values []Evidence, location string) error {
	if len(values) == 0 {
		return failure(ErrorInvalid, "evidence must contain at least one observation", location, nil)
	}
	for index, value := range values {
		if strings.TrimSpace(value.Source) == "" || value.Source != strings.TrimSpace(value.Source) || strings.TrimSpace(value.Observation) == "" || value.Observation != strings.TrimSpace(value.Observation) {
			return failure(ErrorInvalid, "evidence source and observation are required without surrounding whitespace", fmt.Sprintf("%s/%d", location, index), nil)
		}
	}
	return nil
}

func deriveDisposition(findings []Finding, contract *workflow.ReadinessContract) Disposition {
	disposition := DispositionReady
	for _, finding := range findings {
		switch value := finding.(type) {
		case InvariantFinding:
			if value.Status == InvariantViolated {
				return DispositionInvariantBlocked
			}
		case *InvariantFinding:
			if value.Status == InvariantViolated {
				return DispositionInvariantBlocked
			}
		case PolicyGateFinding:
			if value.Status == GateUnsatisfied {
				disposition = gateDisposition(disposition, contract, value.Policy)
			}
		case *PolicyGateFinding:
			if value != nil && value.Status == GateUnsatisfied {
				disposition = gateDisposition(disposition, contract, value.Policy)
			}
		case RecommendationFinding, *RecommendationFinding:
			if disposition == DispositionReady {
				disposition = DispositionChoiceRequired
			}
		}
	}
	return disposition
}

func gateDisposition(current Disposition, contract *workflow.ReadinessContract, policy workflow.Identifier) Disposition {
	for _, gate := range contract.PolicyGates {
		if gate.Policy != policy {
			continue
		}
		if gate.Enforcement == workflow.ReadinessGateAdvisory {
			if current == DispositionReady {
				return DispositionChoiceRequired
			}
			return current
		}
		return DispositionPolicyBlocked
	}
	return current
}

func findingIdentity(finding Finding) (string, []Evidence, bool) {
	switch value := finding.(type) {
	case InformationFinding:
		return value.Code, value.Evidence, true
	case *InformationFinding:
		if value != nil {
			return value.Code, value.Evidence, true
		}
	case RecommendationFinding:
		return value.Code, value.Evidence, true
	case *RecommendationFinding:
		if value != nil {
			return value.Code, value.Evidence, true
		}
	case PolicyGateFinding:
		return value.Code, value.Evidence, true
	case *PolicyGateFinding:
		if value != nil {
			return value.Code, value.Evidence, true
		}
	case InvariantFinding:
		return value.Code, value.Evidence, true
	case *InvariantFinding:
		if value != nil {
			return value.Code, value.Evidence, true
		}
	}
	return "", nil, false
}

func routeContains(route workflow.Route, nodeID workflow.Identifier) bool {
	for _, node := range route.Nodes {
		if node.ID == nodeID {
			return true
		}
	}
	return false
}

func hasRemedy(contract *workflow.ReadinessContract, code string) bool {
	if code == "" {
		return false
	}
	for _, remedy := range contract.Remedies {
		if string(remedy.Code) == code {
			return true
		}
	}
	return false
}

func cloneSubmission(value Submission) Submission {
	encoded, _ := json.Marshal(value)
	var result Submission
	_ = json.Unmarshal(encoded, &result)
	return result
}

func strictDecode(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("assessment value must contain one JSON object")
	}
	return nil
}

func failure(code ErrorCode, message, location string, cause error) *Error {
	return &Error{Code: code, Message: message, Location: location, Cause: cause}
}
