package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	identifierPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	workflowNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9._/-]{0,127}$`)
	semanticVersionPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	bindingSourcePattern    = regexp.MustCompile(`^(run\.input\.[a-z][a-z0-9_]{0,63}|node\.[a-z][a-z0-9_]{0,63}\.output\.[a-z][a-z0-9_]{0,63})$`)
	outputSourcePattern     = regexp.MustCompile(`^node\.[a-z][a-z0-9_]{0,63}\.output\.[a-z][a-z0-9_]{0,63}$`)
	operandReferencePattern = regexp.MustCompile(`^(output\.[a-z][a-z0-9_]{0,63}|input\.[a-z][a-z0-9_]{0,63}|run\.input\.[a-z][a-z0-9_]{0,63})(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)
	digestPattern           = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Decode strictly decodes one v1alpha1 workflow document. It rejects unknown
// fields and contradictory tagged variants before returning a domain value.
func Decode(data []byte) (Document, error) {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return Document{}, fmt.Errorf("decode workflow: %w", err)
	}
	return document, nil
}

// Encode validates the structural typed contract and returns indented JSON.
func Encode(document Document) ([]byte, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode workflow: %w", err)
	}
	if _, err := Decode(encoded); err != nil {
		return nil, fmt.Errorf("encode workflow: %w", err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, encoded, "", "  "); err != nil {
		return nil, fmt.Errorf("indent workflow: %w", err)
	}
	return append(indented.Bytes(), '\n'), nil
}

func (document *Document) UnmarshalJSON(data []byte) error {
	type documentWire struct {
		APIVersion string          `json:"apiVersion"`
		Kind       string          `json:"kind"`
		Metadata   json.RawMessage `json:"metadata"`
		Spec       json.RawMessage `json:"spec"`
	}
	var wire documentWire
	if err := strictDecode(data, &wire); err != nil {
		return err
	}
	if wire.APIVersion != APIVersionV1Alpha1 {
		return fmt.Errorf("apiVersion must be %q", APIVersionV1Alpha1)
	}
	if wire.Kind != KindWorkflow {
		return fmt.Errorf("kind must be %q", KindWorkflow)
	}
	if len(wire.Metadata) == 0 || len(wire.Spec) == 0 {
		return errors.New("metadata and spec are required")
	}

	metadata, err := decodeMetadata(wire.Metadata)
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	spec, err := decodeSpec(wire.Spec)
	if err != nil {
		return fmt.Errorf("spec: %w", err)
	}
	result := Document{APIVersion: wire.APIVersion, Kind: wire.Kind, Metadata: metadata, Spec: spec}
	if err := validateDocument(result); err != nil {
		return err
	}
	*document = result
	return nil
}

func decodeMetadata(data []byte) (Metadata, error) {
	var wire struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		DisplayName json.RawMessage `json:"displayName"`
		Description string          `json:"description"`
	}
	if err := strictDecode(data, &wire); err != nil {
		return Metadata{}, err
	}
	if !workflowNamePattern.MatchString(wire.Name) {
		return Metadata{}, fmt.Errorf("name %q is invalid", wire.Name)
	}
	if !semanticVersionPattern.MatchString(wire.Version) {
		return Metadata{}, fmt.Errorf("version %q is not a supported semantic version", wire.Version)
	}
	displayName, err := decodeOptionalNonEmptyString(wire.DisplayName)
	if err != nil {
		return Metadata{}, fmt.Errorf("displayName: %w", err)
	}
	return Metadata{Name: wire.Name, Version: wire.Version, DisplayName: displayName, Description: wire.Description}, nil
}

func decodeSpec(data []byte) (Spec, error) {
	type specWire struct {
		Inputs        json.RawMessage `json:"inputs"`
		RouteDefaults json.RawMessage `json:"routeDefaults"`
		Nodes         json.RawMessage `json:"nodes"`
	}
	var wire specWire
	if err := strictDecode(data, &wire); err != nil {
		return Spec{}, err
	}
	if len(wire.RouteDefaults) == 0 || len(wire.Nodes) == 0 {
		return Spec{}, errors.New("routeDefaults and nodes are required")
	}

	inputs := make(map[Identifier]ValueDeclaration)
	if len(wire.Inputs) != 0 {
		decoded, err := decodeValueDeclarations(wire.Inputs)
		if err != nil {
			return Spec{}, fmt.Errorf("inputs: %w", err)
		}
		inputs = decoded
	}
	var defaults RouteDefaults
	if err := strictDecode(wire.RouteDefaults, &defaults); err != nil {
		return Spec{}, fmt.Errorf("routeDefaults: %w", err)
	}
	if err := validateIdentifier(defaults.Entry); err != nil {
		return Spec{}, fmt.Errorf("routeDefaults.entry: %w", err)
	}
	if len(defaults.Terminals) == 0 {
		return Spec{}, errors.New("routeDefaults.terminals must contain at least one node")
	}
	if err := validateIdentifierList(defaults.Terminals, true); err != nil {
		return Spec{}, fmt.Errorf("routeDefaults.terminals: %w", err)
	}

	nodes, err := decodeNodes(wire.Nodes)
	if err != nil {
		return Spec{}, fmt.Errorf("nodes: %w", err)
	}
	return Spec{Inputs: inputs, RouteDefaults: defaults, Nodes: nodes}, nil
}

func decodeValueDeclarations(data []byte) (map[Identifier]ValueDeclaration, error) {
	var raw map[string]json.RawMessage
	if err := strictDecode(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("must be an object")
	}
	result := make(map[Identifier]ValueDeclaration, len(raw))
	for name, value := range raw {
		id := Identifier(name)
		if err := validateIdentifier(id); err != nil {
			return nil, err
		}
		var wire struct {
			Type        ValueType       `json:"type"`
			Schema      json.RawMessage `json:"schema"`
			Description string          `json:"description"`
		}
		if err := strictDecode(value, &wire); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := validateValueType(wire.Type); err != nil {
			return nil, fmt.Errorf("%s.type: %w", name, err)
		}
		schema, err := decodeOptionalNonEmptyString(wire.Schema)
		if err != nil {
			return nil, fmt.Errorf("%s.schema: %w", name, err)
		}
		result[id] = ValueDeclaration{Type: wire.Type, Schema: schema, Description: wire.Description}
	}
	return result, nil
}

func decodeOutputDeclarations(data []byte) (map[Identifier]OutputDeclaration, error) {
	var raw map[string]json.RawMessage
	if err := strictDecode(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("must be an object")
	}
	result := make(map[Identifier]OutputDeclaration, len(raw))
	for name, value := range raw {
		id := Identifier(name)
		if err := validateIdentifier(id); err != nil {
			return nil, err
		}
		var wire struct {
			Type        ValueType       `json:"type"`
			Schema      json.RawMessage `json:"schema"`
			Description string          `json:"description"`
			Required    *bool           `json:"required"`
		}
		if err := strictDecode(value, &wire); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := validateValueType(wire.Type); err != nil {
			return nil, fmt.Errorf("%s.type: %w", name, err)
		}
		schema, err := decodeOptionalNonEmptyString(wire.Schema)
		if err != nil {
			return nil, fmt.Errorf("%s.schema: %w", name, err)
		}
		result[id] = OutputDeclaration{Type: wire.Type, Schema: schema, Description: wire.Description, Required: wire.Required}
	}
	return result, nil
}

func decodeNodes(data []byte) (map[Identifier]Node, error) {
	var raw map[string]json.RawMessage
	if err := strictDecode(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("must be an object")
	}
	if len(raw) == 0 {
		return nil, errors.New("at least one node is required")
	}
	result := make(map[Identifier]Node, len(raw))
	for name, value := range raw {
		id := Identifier(name)
		if err := validateIdentifier(id); err != nil {
			return nil, err
		}
		node, err := decodeNode(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		result[id] = node
	}
	return result, nil
}

func decodeNode(data []byte) (Node, error) {
	type nodeWire struct {
		DisplayName    json.RawMessage   `json:"displayName"`
		Type           NodeType          `json:"type"`
		Entry          *bool             `json:"entry"`
		Terminal       *bool             `json:"terminal"`
		Inputs         json.RawMessage   `json:"inputs"`
		Outputs        json.RawMessage   `json:"outputs"`
		Readiness      json.RawMessage   `json:"readiness"`
		Reasoning      json.RawMessage   `json:"reasoning"`
		Gate           json.RawMessage   `json:"gate"`
		Command        json.RawMessage   `json:"command"`
		Approval       json.RawMessage   `json:"approval"`
		Call           json.RawMessage   `json:"call"`
		Validators     []json.RawMessage `json:"validators"`
		Retry          json.RawMessage   `json:"retry"`
		Checkpoint     json.RawMessage   `json:"checkpoint"`
		TransitionMode TransitionMode    `json:"transitionMode"`
		Join           json.RawMessage   `json:"join"`
		Permissions    []string          `json:"permissions"`
		Transitions    []json.RawMessage `json:"transitions"`
	}
	var wire nodeWire
	if err := strictDecode(data, &wire); err != nil {
		return nil, err
	}
	if wire.Entry == nil || wire.Terminal == nil || len(wire.Inputs) == 0 || len(wire.Outputs) == 0 {
		return nil, errors.New("type, entry, terminal, inputs, and outputs are required")
	}
	displayName, err := decodeOptionalNonEmptyString(wire.DisplayName)
	if err != nil {
		return nil, fmt.Errorf("displayName: %w", err)
	}
	if !validNodeType(wire.Type) {
		return nil, fmt.Errorf("unsupported node type %q", wire.Type)
	}

	inputs, err := decodeBindings(wire.Inputs)
	if err != nil {
		return nil, fmt.Errorf("inputs: %w", err)
	}
	outputs, err := decodeOutputDeclarations(wire.Outputs)
	if err != nil {
		return nil, fmt.Errorf("outputs: %w", err)
	}
	readiness, err := decodeReadiness(wire.Readiness)
	if err != nil {
		return nil, fmt.Errorf("readiness: %w", err)
	}
	validators, err := decodeValidators(wire.Validators)
	if err != nil {
		return nil, fmt.Errorf("validators: %w", err)
	}
	retry, err := decodeRetry(wire.Retry)
	if err != nil {
		return nil, fmt.Errorf("retry: %w", err)
	}
	checkpoint, err := decodeCheckpoint(wire.Checkpoint)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: %w", err)
	}
	transitionMode := wire.TransitionMode
	if transitionMode == "" {
		transitionMode = TransitionExclusive
	}
	if transitionMode != TransitionExclusive && transitionMode != TransitionFanout {
		return nil, fmt.Errorf("unsupported transitionMode %q", transitionMode)
	}
	join, err := decodeJoin(wire.Join)
	if err != nil {
		return nil, fmt.Errorf("join: %w", err)
	}
	if err := validateUniqueStrings(wire.Permissions); err != nil {
		return nil, fmt.Errorf("permissions: %w", err)
	}
	transitions, err := decodeTransitions(wire.Transitions)
	if err != nil {
		return nil, fmt.Errorf("transitions: %w", err)
	}
	common := NodeFields{
		DisplayName: displayName, Entry: *wire.Entry, Terminal: *wire.Terminal,
		Inputs: inputs, Outputs: outputs, Readiness: readiness, Validators: validators, Retry: retry,
		Checkpoint: checkpoint, TransitionMode: transitionMode, Join: join,
		Permissions: wire.Permissions, Transitions: transitions,
	}

	executors := map[NodeType]json.RawMessage{
		NodeReasoning: wire.Reasoning, NodeGate: wire.Gate, NodeCommand: wire.Command,
		NodeApproval: wire.Approval, NodeSubworkflow: wire.Call,
	}
	for kind, raw := range executors {
		if kind != wire.Type && len(raw) != 0 {
			return nil, fmt.Errorf("%s settings are invalid for node type %q", kind, wire.Type)
		}
	}
	if len(executors[wire.Type]) == 0 {
		return nil, fmt.Errorf("%s settings are required", wire.Type)
	}

	switch wire.Type {
	case NodeReasoning:
		executor, err := decodeReasoning(wire.Reasoning)
		if err != nil {
			return nil, fmt.Errorf("reasoning: %w", err)
		}
		return ReasoningNode{Common: common, Executor: executor}, nil
	case NodeGate:
		executor, err := decodeGate(wire.Gate)
		if err != nil {
			return nil, fmt.Errorf("gate: %w", err)
		}
		return GateNode{Common: common, Executor: executor}, nil
	case NodeCommand:
		executor, err := decodeCommand(wire.Command)
		if err != nil {
			return nil, fmt.Errorf("command: %w", err)
		}
		return CommandNode{Common: common, Executor: executor}, nil
	case NodeApproval:
		executor, err := decodeApproval(wire.Approval)
		if err != nil {
			return nil, fmt.Errorf("approval: %w", err)
		}
		return ApprovalNode{Common: common, Executor: executor}, nil
	case NodeSubworkflow:
		call, err := decodeSubworkflowCall(wire.Call)
		if err != nil {
			return nil, fmt.Errorf("call: %w", err)
		}
		return SubworkflowNode{Common: common, Call: call}, nil
	default:
		panic("node type validated above")
	}
}

func decodeReadiness(data []byte) (*ReadinessContract, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var contract ReadinessContract
	if err := strictDecode(data, &contract); err != nil {
		return nil, err
	}
	if contract.RecommendedEvidence == nil || contract.PolicyGates == nil || contract.Invariants == nil || contract.Remedies == nil {
		return nil, errors.New("recommendedEvidence, policyGates, invariants, and remedies are required arrays")
	}
	seenEvidence := make(map[Identifier]struct{}, len(contract.RecommendedEvidence))
	for index, evidence := range contract.RecommendedEvidence {
		if err := validateIdentifier(evidence.Role); err != nil || strings.TrimSpace(evidence.Description) == "" {
			return nil, fmt.Errorf("recommendedEvidence[%d] requires a valid role and description", index)
		}
		if _, exists := seenEvidence[evidence.Role]; exists {
			return nil, fmt.Errorf("recommendedEvidence role %q is duplicated", evidence.Role)
		}
		seenEvidence[evidence.Role] = struct{}{}
	}
	seenGates := make(map[Identifier]struct{}, len(contract.PolicyGates))
	for index, gate := range contract.PolicyGates {
		if err := validateIdentifier(gate.Policy); err != nil || strings.TrimSpace(gate.Description) == "" {
			return nil, fmt.Errorf("policyGates[%d] requires a valid policy and description", index)
		}
		switch gate.Enforcement {
		case ReadinessGateAdvisory, ReadinessGateBlocking, ReadinessGateExternal:
		default:
			return nil, fmt.Errorf("policyGates[%d] has unsupported enforcement %q", index, gate.Enforcement)
		}
		if _, exists := seenGates[gate.Policy]; exists {
			return nil, fmt.Errorf("policy gate %q is duplicated", gate.Policy)
		}
		seenGates[gate.Policy] = struct{}{}
	}
	if err := validateUniqueStrings(contract.Invariants); err != nil {
		return nil, fmt.Errorf("invariants: %w", err)
	}
	seenRemedies := make(map[Identifier]struct{}, len(contract.Remedies))
	for index, remedy := range contract.Remedies {
		if err := validateIdentifier(remedy.Code); err != nil {
			return nil, fmt.Errorf("remedies[%d].code: %w", index, err)
		}
		if err := validateIdentifier(remedy.Target); err != nil {
			return nil, fmt.Errorf("remedies[%d].target: %w", index, err)
		}
		switch remedy.Action {
		case ReadinessSupplyInput, ReadinessReviseArtifact, ReadinessClarifyDecision, ReadinessInstallCapability, ReadinessRerunValidation:
		default:
			return nil, fmt.Errorf("remedies[%d] has unsupported action %q", index, remedy.Action)
		}
		if strings.TrimSpace(remedy.Description) == "" {
			return nil, fmt.Errorf("remedies[%d].description is required", index)
		}
		if _, exists := seenRemedies[remedy.Code]; exists {
			return nil, fmt.Errorf("remedy code %q is duplicated", remedy.Code)
		}
		seenRemedies[remedy.Code] = struct{}{}
	}
	return &contract, nil
}

func decodeReasoning(data []byte) (ReasoningExecutor, error) {
	var executor ReasoningExecutor
	if err := strictDecode(data, &executor); err != nil {
		return ReasoningExecutor{}, err
	}
	if strings.TrimSpace(executor.Agent) == "" {
		return ReasoningExecutor{}, errors.New("agent is required")
	}
	if err := validateUniqueStrings(executor.Skills); err != nil {
		return ReasoningExecutor{}, fmt.Errorf("skills: %w", err)
	}
	if err := validateUniqueStrings(executor.Tools); err != nil {
		return ReasoningExecutor{}, fmt.Errorf("tools: %w", err)
	}
	return executor, nil
}

func decodeGate(data []byte) (GateExecutor, error) {
	type gateWire struct {
		Policy    string          `json:"policy"`
		Condition json.RawMessage `json:"condition"`
	}
	var wire gateWire
	if err := strictDecode(data, &wire); err != nil {
		return GateExecutor{}, err
	}
	if strings.TrimSpace(wire.Policy) == "" || len(wire.Condition) == 0 {
		return GateExecutor{}, errors.New("policy and condition are required")
	}
	condition, err := decodePredicate(wire.Condition)
	if err != nil {
		return GateExecutor{}, fmt.Errorf("condition: %w", err)
	}
	return GateExecutor{Policy: wire.Policy, Condition: condition}, nil
}

func decodeCommand(data []byte) (CommandExecutor, error) {
	var executor CommandExecutor
	if err := strictDecode(data, &executor); err != nil {
		return CommandExecutor{}, err
	}
	if len(executor.Argv) == 0 {
		return CommandExecutor{}, errors.New("argv must contain at least one argument")
	}
	if executor.TimeoutSeconds != nil && *executor.TimeoutSeconds == 0 {
		return CommandExecutor{}, errors.New("timeoutSeconds must be positive")
	}
	return executor, nil
}

func decodeApproval(data []byte) (ApprovalExecutor, error) {
	type approvalWire struct {
		Actor             string     `json:"actor"`
		ExternalCondition string     `json:"externalCondition"`
		EvidenceOutput    Identifier `json:"evidenceOutput"`
	}
	var wire approvalWire
	if err := strictDecode(data, &wire); err != nil {
		return nil, err
	}
	if strings.TrimSpace(wire.Actor) == "" {
		return nil, errors.New("actor is required")
	}
	if wire.Actor == "external" {
		if strings.TrimSpace(wire.ExternalCondition) == "" {
			return nil, errors.New("external actor requires externalCondition")
		}
		if err := validateIdentifier(wire.EvidenceOutput); err != nil {
			return nil, fmt.Errorf("external actor requires evidenceOutput: %w", err)
		}
		return ExternalApproval{ExternalCondition: wire.ExternalCondition, EvidenceOutput: wire.EvidenceOutput}, nil
	}
	if wire.ExternalCondition != "" || wire.EvidenceOutput != "" {
		return nil, errors.New("externalCondition and evidenceOutput are valid only for the external actor")
	}
	return NamedApproval{Name: wire.Actor}, nil
}

func decodeSubworkflowCall(data []byte) (SubworkflowCall, error) {
	var call SubworkflowCall
	if err := strictDecode(data, &call); err != nil {
		return SubworkflowCall{}, err
	}
	if strings.TrimSpace(call.Workflow.Name) == "" || strings.TrimSpace(call.Workflow.Version) == "" {
		return SubworkflowCall{}, errors.New("workflow name and version are required")
	}
	if call.Workflow.Digest != "" && !digestPattern.MatchString(call.Workflow.Digest) {
		return SubworkflowCall{}, errors.New("workflow digest must be a lowercase SHA-256")
	}
	if err := validateIdentifier(call.Entry); err != nil {
		return SubworkflowCall{}, fmt.Errorf("entry: %w", err)
	}
	if len(call.Terminals) == 0 {
		return SubworkflowCall{}, errors.New("terminals must contain at least one node")
	}
	if err := validateIdentifierList(call.Terminals, true); err != nil {
		return SubworkflowCall{}, fmt.Errorf("terminals: %w", err)
	}
	if call.Inputs == nil || call.Outputs == nil {
		return SubworkflowCall{}, errors.New("inputs and outputs are required")
	}
	for parent, child := range call.Inputs {
		if err := validateIdentifier(parent); err != nil {
			return SubworkflowCall{}, fmt.Errorf("inputs: %w", err)
		}
		if err := validateIdentifier(child); err != nil {
			return SubworkflowCall{}, fmt.Errorf("inputs.%s: %w", parent, err)
		}
	}
	for output, source := range call.Outputs {
		if err := validateIdentifier(output); err != nil {
			return SubworkflowCall{}, fmt.Errorf("outputs: %w", err)
		}
		if !outputSourcePattern.MatchString(source) {
			return SubworkflowCall{}, fmt.Errorf("outputs.%s has invalid source %q", output, source)
		}
	}
	return call, nil
}

func decodeBindings(data []byte) (map[Identifier]Binding, error) {
	var raw map[string]json.RawMessage
	if err := strictDecode(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("must be an object")
	}
	result := make(map[Identifier]Binding, len(raw))
	for name, value := range raw {
		id := Identifier(name)
		if err := validateIdentifier(id); err != nil {
			return nil, err
		}
		binding, err := decodeBinding(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		result[id] = binding
	}
	return result, nil
}

func decodeBinding(data []byte) (Binding, error) {
	type bindingWire struct {
		From        string          `json:"from"`
		Pointer     string          `json:"pointer"`
		Type        ValueType       `json:"type"`
		Required    *bool           `json:"required"`
		Default     json.RawMessage `json:"default"`
		Description string          `json:"description"`
	}
	var wire bindingWire
	if err := strictDecode(data, &wire); err != nil {
		return nil, err
	}
	if !bindingSourcePattern.MatchString(wire.From) {
		return nil, fmt.Errorf("invalid source %q", wire.From)
	}
	if err := validateValueType(wire.Type); err != nil {
		return nil, err
	}
	if wire.Pointer != "" && !validJSONPointer(wire.Pointer) {
		return nil, fmt.Errorf("invalid JSON pointer %q", wire.Pointer)
	}
	required := wire.Required == nil || *wire.Required
	if required {
		if len(wire.Default) != 0 {
			return nil, errors.New("default requires required=false")
		}
		return RequiredBinding{From: wire.From, Pointer: wire.Pointer, Type: wire.Type, Description: wire.Description}, nil
	}
	return OptionalBinding{From: wire.From, Pointer: wire.Pointer, Type: wire.Type, Default: cloneRaw(wire.Default), Description: wire.Description}, nil
}

func decodeValidators(raw []json.RawMessage) ([]Validator, error) {
	result := make([]Validator, 0, len(raw))
	for index, data := range raw {
		type validatorWire struct {
			Output  Identifier `json:"output"`
			Schema  string     `json:"schema"`
			Command []string   `json:"command"`
		}
		var wire validatorWire
		if err := strictDecode(data, &wire); err != nil {
			return nil, fmt.Errorf("%d: %w", index, err)
		}
		hasSchema := wire.Output != "" || wire.Schema != ""
		hasCommand := wire.Command != nil
		if hasSchema == hasCommand {
			return nil, fmt.Errorf("%d: exactly one schema or command validator is required", index)
		}
		if hasSchema {
			if err := validateIdentifier(wire.Output); err != nil {
				return nil, fmt.Errorf("%d.output: %w", index, err)
			}
			if strings.TrimSpace(wire.Schema) == "" {
				return nil, fmt.Errorf("%d.schema is required", index)
			}
			result = append(result, SchemaValidator{Output: wire.Output, Schema: wire.Schema})
			continue
		}
		if len(wire.Command) == 0 {
			return nil, fmt.Errorf("%d.command must contain at least one argument", index)
		}
		result = append(result, CommandValidator{Command: wire.Command})
	}
	return result, nil
}

func decodeRetry(data []byte) (*RetryPolicy, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var retry RetryPolicy
	if err := strictDecode(data, &retry); err != nil {
		return nil, err
	}
	if retry.MaxAttempts == 0 || retry.MaxAttempts > 100 || retry.On == nil {
		return nil, errors.New("maxAttempts must be 1..100 and on is required")
	}
	seen := make(map[RetryFailure]struct{}, len(retry.On))
	for _, failure := range retry.On {
		if !validRetryFailure(failure) {
			return nil, fmt.Errorf("unsupported failure %q", failure)
		}
		if _, exists := seen[failure]; exists {
			return nil, fmt.Errorf("duplicate failure %q", failure)
		}
		seen[failure] = struct{}{}
	}
	return &retry, nil
}

func decodeCheckpoint(data []byte) (Checkpoint, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '"' {
		var mode CheckpointMode
		if err := strictDecode(data, &mode); err != nil {
			return nil, err
		}
		switch mode {
		case CheckpointNone:
			return NoCheckpoint{}, nil
		case CheckpointAcknowledge:
			return AcknowledgeCheckpoint{}, nil
		case CheckpointApprove:
			return ApproveCheckpoint{}, nil
		default:
			return nil, fmt.Errorf("checkpoint shorthand does not support %q", mode)
		}
	}
	type modeWire struct {
		Mode CheckpointMode `json:"mode"`
	}
	var discriminator modeWire
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return nil, err
	}
	switch discriminator.Mode {
	case CheckpointNone, CheckpointAcknowledge:
		var wire modeWire
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		if wire.Mode == CheckpointNone {
			return NoCheckpoint{}, nil
		}
		return AcknowledgeCheckpoint{}, nil
	case CheckpointApprove:
		var wire struct {
			Mode         CheckpointMode `json:"mode"`
			MaxRevisions *uint8         `json:"maxRevisions"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		if wire.MaxRevisions != nil && *wire.MaxRevisions > 100 {
			return nil, errors.New("maxRevisions must be 0..100")
		}
		return ApproveCheckpoint{MaxRevisions: wire.MaxRevisions}, nil
	case CheckpointApproveOnChange:
		var wire struct {
			Mode         CheckpointMode  `json:"mode"`
			When         json.RawMessage `json:"when"`
			MaxRevisions *uint8          `json:"maxRevisions"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		if len(wire.When) == 0 {
			return nil, errors.New("approve_on_change requires when")
		}
		if wire.MaxRevisions != nil && *wire.MaxRevisions > 100 {
			return nil, errors.New("maxRevisions must be 0..100")
		}
		when, err := decodePredicate(wire.When)
		if err != nil {
			return nil, fmt.Errorf("when: %w", err)
		}
		return ApproveOnChangeCheckpoint{When: when, MaxRevisions: wire.MaxRevisions}, nil
	case CheckpointExternal:
		var wire struct {
			Mode              CheckpointMode `json:"mode"`
			ExternalCondition string         `json:"externalCondition"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		if strings.TrimSpace(wire.ExternalCondition) == "" {
			return nil, errors.New("external checkpoint requires externalCondition")
		}
		return ExternalCheckpoint{ExternalCondition: wire.ExternalCondition}, nil
	default:
		return nil, fmt.Errorf("unsupported checkpoint mode %q", discriminator.Mode)
	}
}

func decodeJoin(data []byte) (*Join, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var join Join
	if err := strictDecode(data, &join); err != nil {
		return nil, err
	}
	if join.Mode != JoinOne && join.Mode != JoinAll {
		return nil, fmt.Errorf("unsupported mode %q", join.Mode)
	}
	if len(join.From) < 2 {
		return nil, errors.New("from must contain at least two nodes")
	}
	if err := validateIdentifierList(join.From, true); err != nil {
		return nil, err
	}
	return &join, nil
}

func decodeTransitions(raw []json.RawMessage) ([]Transition, error) {
	result := make([]Transition, 0, len(raw))
	for index, data := range raw {
		transition, err := decodeTransition(data)
		if err != nil {
			return nil, fmt.Errorf("%d: %w", index, err)
		}
		result = append(result, transition)
	}
	return result, nil
}

func decodeTransition(data []byte) (Transition, error) {
	type transitionWire struct {
		ID               Identifier      `json:"id"`
		To               Identifier      `json:"to"`
		When             json.RawMessage `json:"when"`
		Kind             string          `json:"kind"`
		MaxTraversals    *uint16         `json:"maxTraversals"`
		EnabledByDefault *bool           `json:"enabledByDefault"`
	}
	var wire transitionWire
	if err := strictDecode(data, &wire); err != nil {
		return nil, err
	}
	if err := validateIdentifier(wire.ID); err != nil {
		return nil, fmt.Errorf("id: %w", err)
	}
	if err := validateIdentifier(wire.To); err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	var when Predicate
	var err error
	if len(wire.When) != 0 {
		when, err = decodePredicate(wire.When)
		if err != nil {
			return nil, fmt.Errorf("when: %w", err)
		}
	}
	common := TransitionFields{TransitionID: wire.ID, To: wire.To, When: when, EnabledByDefault: wire.EnabledByDefault}
	switch wire.Kind {
	case "", "normal":
		if wire.MaxTraversals != nil {
			return nil, errors.New("maxTraversals is valid only for a bounded transition")
		}
		return NormalTransition{Common: common}, nil
	case "bounded":
		if wire.MaxTraversals == nil || *wire.MaxTraversals == 0 || *wire.MaxTraversals > 10000 {
			return nil, errors.New("bounded transition requires maxTraversals in 1..10000")
		}
		return BoundedTransition{Common: common, MaxTraversals: *wire.MaxTraversals}, nil
	default:
		return nil, fmt.Errorf("unsupported kind %q", wire.Kind)
	}
}

func decodePredicate(data []byte) (Predicate, error) {
	var discriminator struct {
		Const *bool  `json:"const"`
		Op    string `json:"op"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return nil, err
	}
	if discriminator.Const != nil {
		var wire struct {
			Const bool `json:"const"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		return ConstantPredicate{Value: wire.Const}, nil
	}
	switch ComparisonOperator(discriminator.Op) {
	case CompareEqual, CompareNotEqual, CompareLess, CompareLessOrEqual, CompareGreater, CompareGreaterOrEqual:
		var wire struct {
			Op   ComparisonOperator `json:"op"`
			Args []json.RawMessage  `json:"args"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		if len(wire.Args) != 2 {
			return nil, errors.New("comparison predicate requires exactly two operands")
		}
		left, err := decodeOperand(wire.Args[0])
		if err != nil {
			return nil, fmt.Errorf("args.0: %w", err)
		}
		right, err := decodeOperand(wire.Args[1])
		if err != nil {
			return nil, fmt.Errorf("args.1: %w", err)
		}
		return ComparisonPredicate{Operator: wire.Op, Args: [2]Operand{left, right}}, nil
	}
	if discriminator.Op == "present" {
		var wire struct {
			Op  string          `json:"op"`
			Arg json.RawMessage `json:"arg"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		operand, err := decodeOperand(wire.Arg)
		if err != nil {
			return nil, fmt.Errorf("arg: %w", err)
		}
		reference, ok := operand.(ReferenceOperand)
		if !ok {
			return nil, errors.New("present requires a reference operand")
		}
		return PresentPredicate{Reference: reference}, nil
	}
	if discriminator.Op == string(LogicalAll) || discriminator.Op == string(LogicalAny) {
		var wire struct {
			Op   LogicalOperator   `json:"op"`
			Args []json.RawMessage `json:"args"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		if len(wire.Args) == 0 {
			return nil, errors.New("logical predicate requires at least one argument")
		}
		args := make([]Predicate, 0, len(wire.Args))
		for index, raw := range wire.Args {
			predicate, err := decodePredicate(raw)
			if err != nil {
				return nil, fmt.Errorf("args.%d: %w", index, err)
			}
			args = append(args, predicate)
		}
		return LogicalPredicate{Operator: wire.Op, Args: args}, nil
	}
	if discriminator.Op == "not" {
		var wire struct {
			Op  string          `json:"op"`
			Arg json.RawMessage `json:"arg"`
		}
		if err := strictDecode(data, &wire); err != nil {
			return nil, err
		}
		arg, err := decodePredicate(wire.Arg)
		if err != nil {
			return nil, fmt.Errorf("arg: %w", err)
		}
		return NotPredicate{Arg: arg}, nil
	}
	return nil, fmt.Errorf("unsupported predicate operator %q", discriminator.Op)
}

func decodeOperand(data []byte) (Operand, error) {
	var wire struct {
		Ref     *string         `json:"ref"`
		Literal json.RawMessage `json:"literal"`
	}
	if err := strictDecode(data, &wire); err != nil {
		return nil, err
	}
	if (wire.Ref == nil) == (len(wire.Literal) == 0) {
		return nil, errors.New("exactly one ref or literal is required")
	}
	if wire.Ref != nil {
		if !operandReferencePattern.MatchString(*wire.Ref) {
			return nil, fmt.Errorf("invalid reference %q", *wire.Ref)
		}
		return ReferenceOperand{Ref: *wire.Ref}, nil
	}
	return LiteralOperand{Literal: cloneRaw(wire.Literal)}, nil
}

func validateDocument(document Document) error {
	if document.APIVersion != APIVersionV1Alpha1 || document.Kind != KindWorkflow {
		return errors.New("document has an unsupported apiVersion or kind")
	}
	if !workflowNamePattern.MatchString(document.Metadata.Name) || !semanticVersionPattern.MatchString(document.Metadata.Version) {
		return errors.New("document metadata is invalid")
	}
	if len(document.Spec.Nodes) == 0 || len(document.Spec.RouteDefaults.Terminals) == 0 {
		return errors.New("document requires nodes and route terminals")
	}
	return nil
}

func validateIdentifier(identifier Identifier) error {
	if !identifierPattern.MatchString(string(identifier)) {
		return fmt.Errorf("invalid identifier %q", identifier)
	}
	return nil
}

func validateIdentifierList(values []Identifier, unique bool) error {
	seen := make(map[Identifier]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentifier(value); err != nil {
			return err
		}
		if unique {
			if _, exists := seen[value]; exists {
				return fmt.Errorf("duplicate identifier %q", value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func validateValueType(value ValueType) error {
	switch value {
	case ValueNull, ValueBoolean, ValueInteger, ValueNumber, ValueString, ValueArray, ValueObject:
		return nil
	default:
		return fmt.Errorf("unsupported value type %q", value)
	}
}

func validNodeType(value NodeType) bool {
	switch value {
	case NodeReasoning, NodeGate, NodeCommand, NodeApproval, NodeSubworkflow:
		return true
	default:
		return false
	}
}

func validRetryFailure(value RetryFailure) bool {
	switch value {
	case RetryProviderUnavailable, RetryProviderRateLimit, RetryProcessFailure, RetryValidatorFailure, RetryTimeout, RetryInterrupted:
		return true
	default:
		return false
	}
}

func validateUniqueStrings(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate value %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validJSONPointer(pointer string) bool {
	if pointer == "" {
		return true
	}
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	for index := 0; index < len(pointer); index++ {
		if pointer[index] != '~' {
			continue
		}
		if index+1 >= len(pointer) || (pointer[index+1] != '0' && pointer[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func decodeOptionalNonEmptyString(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return "", errors.New("must be a non-empty string")
	}
	var value string
	if err := strictDecode(data, &value); err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("must be a non-empty string")
	}
	return value, nil
}

func strictDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func nodeObject(common NodeFields, nodeType NodeType, executorName string, executor any) map[string]any {
	result := map[string]any{
		"type": nodeType, "entry": common.Entry, "terminal": common.Terminal,
		"inputs": common.Inputs, "outputs": common.Outputs, executorName: executor,
	}
	if common.DisplayName != "" {
		result["displayName"] = common.DisplayName
	}
	if len(common.Validators) != 0 {
		result["validators"] = common.Validators
	}
	if common.Readiness != nil {
		result["readiness"] = common.Readiness
	}
	if common.Retry != nil {
		result["retry"] = common.Retry
	}
	if common.Checkpoint != nil {
		result["checkpoint"] = common.Checkpoint
	}
	if common.TransitionMode != "" && common.TransitionMode != TransitionExclusive {
		result["transitionMode"] = common.TransitionMode
	}
	if common.Join != nil {
		result["join"] = common.Join
	}
	if len(common.Permissions) != 0 {
		result["permissions"] = common.Permissions
	}
	if len(common.Transitions) != 0 {
		result["transitions"] = common.Transitions
	}
	return result
}

func (node ReasoningNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(node.Common, node.Type(), "reasoning", node.Executor))
}
func (node CommandNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(node.Common, node.Type(), "command", node.Executor))
}
func (node SubworkflowNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(node.Common, node.Type(), "call", node.Call))
}

func (node GateNode) MarshalJSON() ([]byte, error) {
	executor := map[string]any{"policy": node.Executor.Policy, "condition": node.Executor.Condition}
	return json.Marshal(nodeObject(node.Common, node.Type(), "gate", executor))
}

func (node ApprovalNode) MarshalJSON() ([]byte, error) {
	var executor any
	switch approval := node.Executor.(type) {
	case NamedApproval:
		executor = map[string]any{"actor": approval.Name}
	case ExternalApproval:
		executor = map[string]any{"actor": "external", "externalCondition": approval.ExternalCondition, "evidenceOutput": approval.EvidenceOutput}
	default:
		return nil, fmt.Errorf("unsupported approval executor %T", node.Executor)
	}
	return json.Marshal(nodeObject(node.Common, node.Type(), "approval", executor))
}

func (binding RequiredBinding) MarshalJSON() ([]byte, error) {
	value := map[string]any{"from": binding.From, "type": binding.Type}
	if binding.Pointer != "" {
		value["pointer"] = binding.Pointer
	}
	if binding.Description != "" {
		value["description"] = binding.Description
	}
	return json.Marshal(value)
}

func (binding OptionalBinding) MarshalJSON() ([]byte, error) {
	value := map[string]any{"from": binding.From, "type": binding.Type, "required": false}
	if binding.Pointer != "" {
		value["pointer"] = binding.Pointer
	}
	if binding.Default != nil {
		value["default"] = binding.Default
	}
	if binding.Description != "" {
		value["description"] = binding.Description
	}
	return json.Marshal(value)
}

func (NoCheckpoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"mode": CheckpointNone})
}
func (AcknowledgeCheckpoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"mode": CheckpointAcknowledge})
}
func (checkpoint ApproveCheckpoint) MarshalJSON() ([]byte, error) {
	value := map[string]any{"mode": CheckpointApprove}
	if checkpoint.MaxRevisions != nil {
		value["maxRevisions"] = checkpoint.MaxRevisions
	}
	return json.Marshal(value)
}
func (checkpoint ApproveOnChangeCheckpoint) MarshalJSON() ([]byte, error) {
	value := map[string]any{"mode": CheckpointApproveOnChange, "when": checkpoint.When}
	if checkpoint.MaxRevisions != nil {
		value["maxRevisions"] = checkpoint.MaxRevisions
	}
	return json.Marshal(value)
}
func (checkpoint ExternalCheckpoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"mode": CheckpointExternal, "externalCondition": checkpoint.ExternalCondition})
}

func transitionObject(common TransitionFields) map[string]any {
	value := map[string]any{"id": common.TransitionID, "to": common.To}
	if common.When != nil {
		value["when"] = common.When
	}
	if common.EnabledByDefault != nil {
		value["enabledByDefault"] = common.EnabledByDefault
	}
	return value
}
func (transition NormalTransition) MarshalJSON() ([]byte, error) {
	return json.Marshal(transitionObject(transition.Common))
}
func (transition BoundedTransition) MarshalJSON() ([]byte, error) {
	value := transitionObject(transition.Common)
	value["kind"] = "bounded"
	value["maxTraversals"] = transition.MaxTraversals
	return json.Marshal(value)
}

func (predicate ConstantPredicate) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"const": predicate.Value})
}
func (predicate ComparisonPredicate) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"op": predicate.Operator, "args": predicate.Args})
}
func (predicate PresentPredicate) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"op": "present", "arg": predicate.Reference})
}
func (predicate LogicalPredicate) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"op": predicate.Operator, "args": predicate.Args})
}
func (predicate NotPredicate) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"op": "not", "arg": predicate.Arg})
}
func (operand ReferenceOperand) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"ref": operand.Ref})
}
func (operand LiteralOperand) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"literal": operand.Literal})
}
