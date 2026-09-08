// Package preparation selects the smallest deterministically safe route from
// semantic advice bound to immutable work, evidence, workflow and policy inputs.
package preparation

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/routeadvisor"
	"darkstar/src/ports/statestore"
)

// Policy is a snapshot, not a mutable gate. RequiredNodes cannot be bypassed
// even by human confirmation. Assumptions and low confidence require a decision.
type Policy struct {
	Version            string                `json:"version"`
	RequiredNodes      []workflow.Identifier `json:"requiredNodes"`
	ConsequentialNodes []workflow.Identifier `json:"consequentialNodes"`
	AllowedAssumptions []string              `json:"allowedAssumptions"`
}

type Input struct {
	Work           statestore.WorkItemProjection `json:"work"`
	Project        statestore.ProjectProjection  `json:"project"`
	Workflow       workflow.Document             `json:"workflow"`
	WorkflowDigest string                        `json:"workflowDigest"`
	Policy         Policy                        `json:"policy"`
	Context        workflow.RouteContext         `json:"context"`
	Answers        map[string]string             `json:"answers"`
	Evidence       []routeadvisor.Evidence       `json:"evidence"`
	Override       *workflow.RouteRequest        `json:"override,omitempty"`
}

type Alternative struct {
	Entry      workflow.Identifier         `json:"entry"`
	Terminals  []workflow.Identifier       `json:"terminals"`
	NodeCount  int                         `json:"nodeCount"`
	Rationale  string                      `json:"rationale"`
	Validation workflow.ValidationErrors   `json:"validation"`
	Missing    []workflow.InputRequirement `json:"missing"`
}

// Assessment has one immutable selected route. Readiness is derived from
// questions and confirmation reasons; clients cannot set a competing ready flag.
type Assessment struct {
	SchemaVersion       int                     `json:"schemaVersion"`
	Input               Input                   `json:"input"`
	InputDigest         string                  `json:"inputDigest"`
	Advice              routeadvisor.Advice     `json:"advice"`
	Route               workflow.Route          `json:"route"`
	Rationale           string                  `json:"rationale"`
	Questions           []routeadvisor.Question `json:"questions"`
	ConfirmationReasons []string                `json:"confirmationReasons"`
	Alternatives        []Alternative           `json:"alternatives"`
	Digest              string                  `json:"digest"`
}

func (a Assessment) Readiness() string {
	if len(a.Questions) > 0 || len(a.Route.InputRequirements) > 0 {
		return "input_required"
	}
	if len(a.ConfirmationReasons) > 0 {
		return "confirmation_required"
	}
	return "ready"
}

func Digest(value any) string {
	content, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

// Candidates enumerates bounded authored boundaries: single terminal-capable
// nodes and exact default/profile terminal sets. It never invents graph edges.
func Candidates(input Input) (routeadvisor.Request, []Alternative) {
	request := routeadvisor.Request{Digest: Digest(input), Outcome: input.Work.Title, Details: input.Work.Details, Answers: input.Answers, Evidence: input.Evidence, Candidates: []routeadvisor.Candidate{}}
	request.Context = routeadvisor.PlanningContext{ProjectID: input.Project.ProjectID, ProjectName: input.Project.Name, DefaultEntry: string(input.Workflow.Spec.RouteDefaults.Entry), DefaultTerminals: identifierStrings(input.Workflow.Spec.RouteDefaults.Terminals), RunInputs: map[string]json.RawMessage{}}
	for id, value := range input.Context.RunInputs {
		request.Context.RunInputs[string(id)] = value
	}
	entries := []workflow.Identifier{}
	for id, node := range input.Workflow.Spec.Nodes {
		if node.Fields().Entry {
			entries = append(entries, id)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i] < entries[j] })
	context := input.Context
	context.RequiredNodes = append(append([]workflow.Identifier{}, context.RequiredNodes...), input.Policy.RequiredNodes...)
	alternatives := []Alternative{}
	for _, entry := range entries {
		for _, boundaries := range terminalSets(input.Workflow) {
			route, issues := workflow.CreateRoute(input.Workflow, workflow.RouteRequest{From: entry, Until: boundaries}, context)
			alternatives = append(alternatives, Alternative{Entry: entry, Terminals: boundaries, NodeCount: len(route.Nodes), Validation: issues, Missing: route.InputRequirements})
			if len(issues) > 0 || len(route.InputRequirements) > 0 {
				continue
			}
			nodes := []string{}
			contracts := map[string]any{}
			for _, n := range route.Nodes {
				nodes = append(nodes, string(n.ID))
				fields := input.Workflow.Spec.Nodes[n.ID].Fields()
				contracts[string(n.ID)] = map[string]any{"type": input.Workflow.Spec.Nodes[n.ID].Type(), "execution": input.Workflow.Spec.Nodes[n.ID], "name": fields.DisplayName, "inputs": fields.Inputs, "outputs": fields.Outputs, "readiness": fields.Readiness}
			}
			terminals := []string{}
			for _, id := range route.Terminals {
				terminals = append(terminals, string(id))
			}
			encoded, _ := json.Marshal(contracts)
			request.Candidates = append(request.Candidates, routeadvisor.Candidate{Entry: string(entry), Terminals: terminals, Nodes: nodes, Contracts: encoded})
		}
	}
	return request, alternatives
}

func terminalSets(document workflow.Document) [][]workflow.Identifier {
	sets := map[string][]workflow.Identifier{}
	add := func(ids []workflow.Identifier) {
		stringsValue := []string{}
		for _, id := range ids {
			stringsValue = append(stringsValue, string(id))
		}
		sort.Strings(stringsValue)
		unique := []workflow.Identifier{}
		last := ""
		for _, id := range stringsValue {
			if id != last {
				unique = append(unique, workflow.Identifier(id))
				last = id
			}
		}
		if len(unique) > 0 {
			sets[Digest(unique)] = unique
		}
	}
	add(document.Spec.RouteDefaults.Terminals)
	for id, node := range document.Spec.Nodes {
		if node.Fields().Terminal {
			add([]workflow.Identifier{id})
		}
	}
	for _, profile := range document.Spec.Profiles {
		add(profile.Terminals)
	}
	result := [][]workflow.Identifier{}
	for _, set := range sets {
		result = append(result, set)
	}
	sort.Slice(result, func(i, j int) bool {
		return boundaryKey("", identifierStrings(result[i])) < boundaryKey("", identifierStrings(result[j]))
	})
	return result
}

func identifierStrings(ids []workflow.Identifier) []string {
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = string(id)
	}
	return values
}
func boundaryKey(entry string, terminals []string) string {
	values := append([]string(nil), terminals...)
	sort.Strings(values)
	return entry + "|" + strings.Join(values, ",")
}
func adviceKey(item routeadvisor.CandidateAdvice) string {
	return boundaryKey(item.Entry, item.Terminals)
}

func Assess(input Input, advice routeadvisor.Advice) (Assessment, error) {
	if input.Policy.Version == "" || input.WorkflowDigest == "" {
		return Assessment{}, errors.New("preparation requires exact workflow and policy snapshots")
	}
	for _, id := range append(append([]workflow.Identifier{}, input.Policy.RequiredNodes...), input.Policy.ConsequentialNodes...) {
		if _, ok := input.Workflow.Spec.Nodes[id]; !ok {
			return Assessment{}, fmt.Errorf("project routing policy names absent node %q", id)
		}
	}
	request, alternatives := Candidates(input)
	a := Assessment{SchemaVersion: 1, Input: input, InputDigest: request.Digest, Advice: advice, Alternatives: alternatives, Questions: []routeadvisor.Question{}, ConfirmationReasons: []string{}}
	context := input.Context
	context.RequiredNodes = append(append([]workflow.Identifier{}, context.RequiredNodes...), input.Policy.RequiredNodes...)
	if input.Override != nil {
		route, issues := workflow.CreateRoute(input.Workflow, *input.Override, context)
		if len(issues) > 0 {
			return Assessment{}, fmt.Errorf("routing override is invalid or prohibited: %w", issues)
		}
		a.Route = route
		a.Rationale = "Preserved the explicit routing override."
	} else {
		if advice.Confidence != "high" && advice.Confidence != "medium" && advice.Confidence != "low" {
			return Assessment{}, errors.New("advisor confidence must be high, medium or low")
		}
		eligible := map[string]routeadvisor.Candidate{}
		for _, candidate := range request.Candidates {
			eligible[boundaryKey(candidate.Entry, candidate.Terminals)] = candidate
		}
		seen := map[string]bool{}
		suitable := []routeadvisor.CandidateAdvice{}
		for _, item := range advice.Candidates {
			key := adviceKey(item)
			if _, ok := eligible[key]; !ok || seen[key] {
				return Assessment{}, fmt.Errorf("advisor proposed invalid, unready or duplicate route %q", key)
			}
			seen[key] = true
			if strings.TrimSpace(item.Rationale) == "" {
				return Assessment{}, errors.New("advisor must explain every candidate")
			}
			switch item.Disposition {
			case "suitable":
				if len(item.Questions) > 0 {
					return Assessment{}, errors.New("suitable advice cannot contain missing questions")
				}
				suitable = append(suitable, item)
			case "unsuitable":
			case "input_required":
				if len(item.Questions) == 0 {
					return Assessment{}, errors.New("input_required advice needs targeted questions")
				}
			default:
				return Assessment{}, errors.New("unknown candidate disposition")
			}
			for i := range a.Alternatives {
				if boundaryKey(string(a.Alternatives[i].Entry), identifierStrings(a.Alternatives[i].Terminals)) == key {
					a.Alternatives[i].Rationale = item.Rationale
				}
			}
		}
		if len(seen) != len(eligible) {
			return Assessment{}, errors.New("advisor must consider every deterministically valid candidate")
		}
		for _, reference := range advice.EvidenceUsed {
			found := false
			for _, e := range input.Evidence {
				if e.Reference == reference && e.Digest != "" && e.Content != "" {
					found = true
				}
			}
			if !found {
				return Assessment{}, fmt.Errorf("advisor cited unresolved evidence %q", reference)
			}
		}
		sort.Slice(suitable, func(i, j int) bool {
			left, right := eligible[adviceKey(suitable[i])], eligible[adviceKey(suitable[j])]
			if len(left.Nodes) == len(right.Nodes) {
				return boundaryKey(left.Entry, left.Terminals) < boundaryKey(right.Entry, right.Terminals)
			}
			return len(left.Nodes) < len(right.Nodes)
		})
		if len(suitable) > 0 {
			selected := suitable[0]
			candidate := eligible[adviceKey(selected)]
			boundaries := []workflow.Identifier{}
			for _, id := range candidate.Terminals {
				boundaries = append(boundaries, workflow.Identifier(id))
			}
			a.Route, _ = workflow.CreateRoute(input.Workflow, workflow.RouteRequest{From: workflow.Identifier(selected.Entry), Until: boundaries}, context)
			a.Rationale = selected.Rationale
			defaultRoute, defaultIssues := workflow.CreateRoute(input.Workflow, workflow.RouteRequest{}, input.Context)
			defaultNodes := map[workflow.Identifier]bool{}
			for _, node := range defaultRoute.Nodes {
				defaultNodes[node.ID] = true
			}
			for _, node := range a.Route.Nodes {
				if len(defaultIssues) > 0 || !defaultNodes[node.ID] {
					a.ConfirmationReasons = append(a.ConfirmationReasons, "Selected outcome extends beyond the workflow default scope.")
					break
				}
			}
			if advice.Confidence != "high" {
				a.ConfirmationReasons = append(a.ConfirmationReasons, "Semantic outcome/readiness confidence is "+advice.Confidence+".")
			}
			for _, assumption := range selected.Assumptions {
				if !contains(input.Policy.AllowedAssumptions, assumption) {
					a.ConfirmationReasons = append(a.ConfirmationReasons, "Confirm assumption: "+assumption)
				}
			}
		} else {
			route, issues := workflow.CreateRoute(input.Workflow, workflow.RouteRequest{}, context)
			if len(issues) > 0 {
				return Assessment{}, issues
			}
			a.Route = route
			a.Rationale = "No safe, outcome-complete candidate is established."
			// Ask about one proposed route, never aggregate mutually exclusive alternatives.
			pending := []routeadvisor.CandidateAdvice{}
			for _, item := range advice.Candidates {
				if item.Disposition == "input_required" {
					pending = append(pending, item)
				}
			}
			defaultKey := boundaryKey(string(route.Entry), identifierStrings(route.Terminals))
			sort.Slice(pending, func(i, j int) bool {
				left, right := adviceKey(pending[i]), adviceKey(pending[j])
				if (left == defaultKey) != (right == defaultKey) {
					return left == defaultKey
				}
				if len(pending[i].Questions) != len(pending[j].Questions) {
					return len(pending[i].Questions) < len(pending[j].Questions)
				}
				return left < right
			})
			if len(pending) > 0 {
				selected := pending[0]
				boundaries := []workflow.Identifier{}
				for _, id := range selected.Terminals {
					boundaries = append(boundaries, workflow.Identifier(id))
				}
				a.Route, _ = workflow.CreateRoute(input.Workflow, workflow.RouteRequest{From: workflow.Identifier(selected.Entry), Until: boundaries}, context)
				a.Rationale = selected.Rationale
				a.Questions = append(a.Questions, selected.Questions...)
			}

			if len(a.Questions) == 0 {
				a.Questions = append(a.Questions, routeadvisor.Question{ID: "outcome", Prompt: "Clarify the requested deliverable and acceptance criteria so a safe route can be selected."})
			}
		}
	}
	for _, requirement := range a.Route.InputRequirements {
		a.Questions = append(a.Questions, routeadvisor.Question{ID: string(requirement.Node) + "." + string(requirement.Input), Prompt: fmt.Sprintf("Supply %s required by %s input %s.", requirement.Source, requirement.Node, requirement.Input)})
	}
	for _, n := range a.Route.Nodes {
		if containsID(input.Policy.ConsequentialNodes, n.ID) {
			a.ConfirmationReasons = append(a.ConfirmationReasons, "Route crosses consequential node "+string(n.ID)+".")
		}
	}
	questions := make([]routeadvisor.Question, 0, len(a.Questions))
	seenQuestions := map[string]string{}
	for _, q := range a.Questions {
		if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Prompt) == "" {
			return Assessment{}, errors.New("targeted questions require identifiers and prompts")
		}
		if prompt, seen := seenQuestions[q.ID]; seen {
			if prompt != q.Prompt {
				return Assessment{}, fmt.Errorf("question %q has conflicting prompts", q.ID)
			}
			continue
		}
		seenQuestions[q.ID] = q.Prompt
		questions = append(questions, q)
	}
	a.Questions = questions
	a.Digest = Digest(a)
	return a, nil
}

func Verify(a Assessment) error {
	digest := a.Digest
	a.Digest = ""
	if Digest(a) != digest {
		return errors.New("immutable preparation assessment content was changed")
	}
	rebuilt, err := Assess(a.Input, a.Advice)
	if err != nil {
		return err
	}
	if rebuilt.Digest != digest {
		return errors.New("immutable preparation assessment digest mismatch")
	}
	return nil
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func containsID(values []workflow.Identifier, value workflow.Identifier) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
