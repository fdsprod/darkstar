package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"darkstar/src/core/contentlibrary"
)

// AssessmentRouterPattern is an authoring operation, not an execution agent.
// Its output contains ordinary reasoning and gate visits, preserving durable
// assessment evidence before the daemon chooses an authored route.
type AssessmentRouterPattern struct {
	Document   json.RawMessage   `json:"document"`
	ID         Identifier        `json:"id,omitempty"`
	Agent      string            `json:"agent,omitempty"`
	InputNames []Identifier      `json:"inputNames,omitempty"`
	Targets    AssessmentTargets `json:"targets"`
}

type AssessmentTargets struct {
	Questions       Identifier `json:"questions"`
	Research        Identifier `json:"research"`
	Design          Identifier `json:"design"`
	TechnicalDesign Identifier `json:"technical_design"`
	Plan            Identifier `json:"plan"`
}

type AssessmentPatternResult struct {
	Document     json.RawMessage `json:"document"`
	AssessmentID Identifier      `json:"assessmentId"`
	GateIDs      []Identifier    `json:"gateIds"`
}

func BuildAssessmentRouter(request AssessmentRouterPattern) (AssessmentPatternResult, error) {
	if request.ID == "" {
		request.ID = "assessment"
	}
	if validateIdentifier(request.ID) != nil || len(request.ID) > 35 {
		return AssessmentPatternResult{}, errors.New("assessment id must be an identifier of at most 35 characters")
	}
	if request.Agent == "" {
		request.Agent = "assessment"
	}
	decoded, err := Decode(request.Document)
	if err != nil {
		return AssessmentPatternResult{}, err
	}
	targets := []Identifier{request.Targets.Questions, request.Targets.Research, request.Targets.Design, request.Targets.TechnicalDesign, request.Targets.Plan}
	seen := map[Identifier]bool{}
	for _, target := range targets {
		node, exists := decoded.Spec.Nodes[target]
		if !exists || seen[target] {
			return AssessmentPatternResult{}, errors.New("select five distinct existing stage nodes")
		}
		if node.Fields().Join != nil && node.Fields().Join.Mode != JoinOne {
			return AssessmentPatternResult{}, fmt.Errorf("target %s has an all join and cannot merge alternative entry paths", target)
		}
		seen[target] = true
	}
	var document map[string]any
	if err := json.Unmarshal(request.Document, &document); err != nil {
		return AssessmentPatternResult{}, err
	}
	spec := document["spec"].(map[string]any)
	nodes := spec["nodes"].(map[string]any)
	inputs, ok := spec["inputs"].(map[string]any)
	if !ok {
		inputs = map[string]any{}
		spec["inputs"] = inputs
	}
	prefix := string(request.ID)
	gates := []Identifier{Identifier(prefix + "_questions_gate"), Identifier(prefix + "_research_gate"), Identifier(prefix + "_design_gate"), Identifier(prefix + "_technical_gate")}
	for _, id := range append([]Identifier{request.ID}, gates...) {
		if _, exists := nodes[string(id)]; exists {
			return AssessmentPatternResult{}, fmt.Errorf("node %s already exists; choose another assessment id", id)
		}
	}
	// Human-supplied approval attestations are frozen run inputs. No agent output
	// is copied into them. An omitted attestation conservatively requires review.
	for _, suffix := range []string{"design_approved", "technical_design_approved"} {
		key := prefix + "_" + suffix
		if _, exists := inputs[key]; exists {
			return AssessmentPatternResult{}, fmt.Errorf("input %s already exists", key)
		}
		inputs[key] = map[string]any{"type": "boolean", "description": "Explicit human attestation that the supplied " + suffix + " artifact is approved; omitted means review required."}
	}
	bound := map[string]any{}
	inputNames := request.InputNames
	if len(inputNames) == 0 {
		inputNames = []Identifier{"task", "story", "context", "repository", "open_items", "deferred_work", "design", "technical_design"}
	}
	for _, id := range inputNames {
		declaration, exists := decoded.Spec.Inputs[id]
		if !exists {
			if len(request.InputNames) != 0 {
				return AssessmentPatternResult{}, fmt.Errorf("assessment input %s is not declared", id)
			}
			continue
		}
		bound[string(id)] = map[string]any{"from": "run.input." + string(id), "type": declaration.Type, "required": false}
	}
	readinessSchema := assessmentReadinessSchema()
	for _, suffix := range []string{"task", "template"} {
		if _, exists := inputs[prefix+"_"+suffix]; exists {
			return AssessmentPatternResult{}, fmt.Errorf("input %s already exists", prefix+"_"+suffix)
		}
	}
	inputs[prefix+"_task"] = map[string]any{"type": "task", "resource": map[string]any{"kind": "task"}}
	inputs[prefix+"_template"] = map[string]any{"type": "template", "resource": map[string]any{"kind": "template_reference", "reference": contentlibrary.BuiltinReference("assessment", "template")}}
	bound["task"] = map[string]any{"from": "run.input." + prefix + "_task", "type": "task"}
	bound["template"] = map[string]any{"from": "run.input." + prefix + "_template", "type": "template"}
	nodes[prefix] = map[string]any{
		"type": "reasoning", "displayName": "Assessment Router", "entry": true, "terminal": false,
		"inputs": bound,
		"prompt": contentlibrary.BuiltinReference("assessment", "prompt"),
		"outputs": map[string]any{
			"readiness": map[string]any{"type": "schema:planning_readiness_v1", "schemaDefinition": readinessSchema},
			"document":  map[string]any{"type": "markdown", "artifact": map[string]any{"filename": "assessment.md", "templateInput": "template"}},
			"findings":  map[string]any{"type": "schema:planning_findings_v1", "schemaDefinition": trackingFindingsSchema()},
		},
		"reasoning":   map[string]any{"agent": request.Agent},
		"checkpoint":  map[string]any{"mode": "none"},
		"transitions": []any{map[string]any{"id": prefix + "_assessed", "to": gates[0]}},
	}
	fields := []string{"questions_ready", "research_ready", "design_ready", "technical_design_ready"}
	for index, id := range gates {
		gateInputs := map[string]any{"readiness": map[string]any{"from": "node." + prefix + ".output.readiness", "type": "schema:planning_readiness_v1"}}
		condition := map[string]any{"op": "eq", "args": []any{map[string]any{"ref": "input.readiness." + fields[index]}, map[string]any{"literal": true}}}
		if index >= 2 {
			approval := prefix + "_design_approved"
			if index == 3 {
				approval = prefix + "_technical_design_approved"
			}
			gateInputs["approved"] = map[string]any{"from": "run.input." + approval, "type": "boolean", "required": false, "default": false}
			condition = map[string]any{"op": "all", "args": []any{condition, map[string]any{"op": "eq", "args": []any{map[string]any{"ref": "input.approved"}, map[string]any{"literal": true}}}}}
		}
		next := targets[4]
		if index < 3 {
			next = gates[index+1]
		}
		nodes[string(id)] = map[string]any{
			"type": "gate", "displayName": "Assessment: " + fields[index], "entry": false, "terminal": false,
			"inputs":     gateInputs,
			"outputs":    map[string]any{"passed": map[string]any{"type": "boolean"}, "gate_evidence": map[string]any{"type": "schema:planning_gate_evidence_v1", "schemaDefinition": json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"policy":{"type":"string"},"passed":{"type":"boolean"}},"required":["policy","passed"]}`)}},
			"gate":       map[string]any{"policy": "assessment-entry-v1", "condition": condition},
			"checkpoint": map[string]any{"mode": "none"},
			"transitions": []any{
				map[string]any{"id": string(id) + "_ready", "to": next, "when": map[string]any{"op": "eq", "args": []any{map[string]any{"ref": "output.passed"}, map[string]any{"literal": true}}}},
				map[string]any{"id": string(id) + "_needed", "to": targets[index], "when": map[string]any{"op": "eq", "args": []any{map[string]any{"ref": "output.passed"}, map[string]any{"literal": false}}}},
			},
		}
	}
	for _, target := range targets {
		incoming := []string{}
		for _, value := range nodes {
			node := value.(map[string]any)
			transitions, _ := node["transitions"].([]any)
			for _, value := range transitions {
				transition := value.(map[string]any)
				if fmt.Sprint(transition["to"]) == string(target) {
					incoming = append(incoming, fmt.Sprint(transition["id"]))
				}
			}
		}
		if len(incoming) > 1 {
			sort.Strings(incoming)
			nodes[string(target)].(map[string]any)["join"] = map[string]any{"mode": "one", "from": incoming}
		}
	}
	for _, target := range targets[2:] {
		node := nodes[string(target)].(map[string]any)
		checkpoint, _ := node["checkpoint"].(map[string]any)
		if checkpoint["mode"] != "approve" {
			node["checkpoint"] = map[string]any{"mode": "approve"}
		}
	}
	spec["routeDefaults"].(map[string]any)["entry"] = request.ID
	document["apiVersion"] = APIVersionV1Alpha3
	encoded, err := json.Marshal(document)
	if err != nil {
		return AssessmentPatternResult{}, err
	}
	result, err := Decode(encoded)
	if err != nil {
		return AssessmentPatternResult{}, err
	}
	if issues := Validate(result); len(issues) != 0 {
		return AssessmentPatternResult{}, issues
	}
	return AssessmentPatternResult{Document: encoded, AssessmentID: request.ID, GateIDs: gates}, nil
}
