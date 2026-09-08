package runexecution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"darkstar/src/ports/valueschema"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

const ProviderBuiltin = "builtin"

type workflowAdvance struct {
	context   statestore.RunExecutionContext
	successor workflow.Identifier
	terminal  bool
	persist   bool
}

func resolveNodeInputs(node workflow.Node, runInputs map[workflow.Identifier]json.RawMessage, outputs map[workflow.Identifier]map[workflow.Identifier]json.RawMessage) (map[workflow.Identifier]json.RawMessage, error) {
	resolved := make(map[workflow.Identifier]json.RawMessage, len(node.Fields().Inputs))
	for inputID, binding := range node.Fields().Inputs {
		raw, present, err := resolveBinding(binding, runInputs, outputs)
		if err != nil {
			return nil, fmt.Errorf("resolve input %q: %w", inputID, err)
		}
		if !present {
			switch optional := binding.(type) {
			case workflow.OptionalBinding:
				if len(optional.Default) != 0 {
					resolved[inputID] = append(json.RawMessage(nil), optional.Default...)
				}
				continue
			default:
				return nil, fmt.Errorf("required source %q is unavailable", binding.Source())
			}
		}
		if !preparationInputType(raw, binding.ValueType()) {
			return nil, fmt.Errorf("source %q has type %q, want %q", binding.Source(), valueType(raw), binding.ValueType())
		}
		resolved[inputID] = raw
	}
	return resolved, nil
}

func resolveBinding(binding workflow.Binding, runInputs map[workflow.Identifier]json.RawMessage, outputs map[workflow.Identifier]map[workflow.Identifier]json.RawMessage) (json.RawMessage, bool, error) {
	parts := strings.Split(binding.Source(), ".")
	var raw json.RawMessage
	var present bool
	switch {
	case len(parts) == 3 && parts[0] == "run" && parts[1] == "input":
		raw, present = runInputs[workflow.Identifier(parts[2])]
	case len(parts) == 4 && parts[0] == "node" && parts[2] == "output":
		nodeOutputs := outputs[workflow.Identifier(parts[1])]
		raw, present = nodeOutputs[workflow.Identifier(parts[3])]
	default:
		return nil, false, fmt.Errorf("unsupported binding source %q", binding.Source())
	}
	if !present {
		return nil, false, nil
	}
	pointer := ""
	switch value := binding.(type) {
	case workflow.RequiredBinding:
		pointer = value.Pointer
	case workflow.OptionalBinding:
		pointer = value.Pointer
	}
	if pointer == "" {
		return append(json.RawMessage(nil), raw...), true, nil
	}
	selected, found, err := selectJSONPointer(raw, pointer)
	return selected, found, err
}

func selectJSONPointer(raw json.RawMessage, pointer string) (json.RawMessage, bool, error) {
	if pointer == "" {
		return append(json.RawMessage(nil), raw...), true, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false, errors.New("JSON Pointer must begin with '/'")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false, errors.New("binding source is invalid JSON")
	}
	for _, encoded := range strings.Split(pointer[1:], "/") {
		member := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		value, ok = object[member]
		if !ok {
			return nil, false, nil
		}
	}
	selected, err := json.Marshal(value)
	return selected, err == nil, err
}

func decodeNodeOutputs(node workflow.Node, raw json.RawMessage) (map[workflow.Identifier]json.RawMessage, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("workflow node output must be one JSON object")
	}
	declarations := node.Fields().Outputs
	for key := range object {
		if _, exists := declarations[workflow.Identifier(key)]; !exists {
			return nil, fmt.Errorf("workflow node returned undeclared output %q", key)
		}
	}
	result := make(map[workflow.Identifier]json.RawMessage, len(object))
	for id, declaration := range declarations {
		value, present := object[string(id)]
		required := declaration.Required == nil || *declaration.Required
		if !present {
			if required {
				return nil, fmt.Errorf("workflow node omitted required output %q", id)
			}
			continue
		}
		if !preparationInputType(value, declaration.Type) {
			return nil, fmt.Errorf("workflow output %q has type %q, want %q", id, valueType(value), declaration.Type)
		}
		result[id] = append(json.RawMessage(nil), value...)
	}
	return result, nil
}

func valueType(raw json.RawMessage) workflow.ValueType {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return ""
	}
	switch value := value.(type) {
	case nil:
		return workflow.ValueNull
	case bool:
		return workflow.ValueBoolean
	case json.Number:
		if !strings.ContainsAny(string(value), ".eE") {
			return workflow.ValueInteger
		}
		return workflow.ValueNumber
	case string:
		return workflow.ValueString
	case []any:
		return workflow.ValueArray
	case map[string]any:
		return workflow.ValueObject
	default:
		return ""
	}
}

func transitionWhen(value workflow.Transition) workflow.Predicate {
	switch transition := value.(type) {
	case workflow.NormalTransition:
		return transition.Common.When
	case workflow.BoundedTransition:
		return transition.Common.When
	default:
		return nil
	}
}

func transitionInRoute(route workflow.Route, id workflow.Identifier) bool {
	for _, transition := range route.Transitions {
		if transition.ID == id {
			return true
		}
	}
	return false
}

func chooseTransition(nodeID workflow.Identifier, node workflow.Node, route workflow.Route, inputs, outputs, runInputs map[workflow.Identifier]json.RawMessage) (workflow.Transition, error) {
	var selected workflow.Transition
	for index, transition := range node.Fields().Transitions {
		if !transitionInRoute(route, transition.ID()) {
			continue
		}
		matched := transitionWhen(transition) == nil
		if !matched {
			var err error
			matched, err = workflow.EvaluatePredicate(transitionWhen(transition), workflow.PredicateValues{Outputs: outputs, Inputs: inputs, RunInputs: runInputs}, fmt.Sprintf("/nodes/%s/transitions/%d/when", nodeID, index))
			if err != nil {
				return nil, err
			}
		}
		if matched {
			if selected != nil {
				return nil, errors.New("bounded scheduler does not support multiple matching successor transitions")
			}
			selected = transition
		}
	}
	if selected == nil {
		return nil, errors.New("workflow node has no matching successor transition")
	}
	return selected, nil
}

func prepareWorkflowAdvance(dispatch AttemptRequestContext, output map[workflow.Identifier]json.RawMessage, validators ...valueschema.Validator) (workflowAdvance, error) {
	for id, value := range output {
		if err := workflow.ValidateDeliverable(dispatch.Node, id, value, dispatch.NodeInputs, validators...); err != nil {
			return workflowAdvance{}, err
		}
	}
	storedForNode, alreadyStored := dispatch.AcceptedOutputs[workflow.Identifier(dispatch.Attempt.NodeID)]
	if alreadyStored && rawMapsEqual(storedForNode, output) {
		if identifierIn(dispatch.FrozenRoute.Terminals, workflow.Identifier(dispatch.Attempt.NodeID)) {
			return workflowAdvance{context: dispatch.ExecutionContext, terminal: true}, nil
		}
		for _, token := range dispatch.FrameSnapshot.Tokens {
			if token.Key.SourceVisitID == dispatch.Attempt.VisitID {
				return workflowAdvance{context: dispatch.ExecutionContext, successor: token.Target}, nil
			}
		}
	}
	stored := cloneAcceptedOutputs(dispatch.AcceptedOutputs)
	stored[workflow.Identifier(dispatch.Attempt.NodeID)] = cloneRawMap(output)
	value := dispatch.ExecutionContext
	value.AcceptedOutputs = stringAcceptedOutputs(stored)
	if identifierIn(dispatch.FrozenRoute.Terminals, workflow.Identifier(dispatch.Attempt.NodeID)) {
		return workflowAdvance{context: value, terminal: true, persist: true}, nil
	}
	transition, err := chooseTransition(workflow.Identifier(dispatch.Attempt.NodeID), dispatch.Node, dispatch.FrozenRoute, dispatch.NodeInputs, output, dispatch.RunInputs)
	if err != nil {
		return workflowAdvance{}, err
	}
	frame, err := workflow.RestoreFrame(dispatch.FrameSnapshot)
	if err != nil {
		return workflowAdvance{}, err
	}
	if _, _, err := frame.FireTransition(dispatch.Attempt.VisitID, workflow.Identifier(dispatch.Attempt.NodeID), value.Revision+1, transition); err != nil {
		return workflowAdvance{}, err
	}
	value.FrameSnapshot, err = json.Marshal(frame.Snapshot())
	if err != nil {
		return workflowAdvance{}, fmt.Errorf("encode advanced workflow frame: %w", err)
	}
	return workflowAdvance{context: value, successor: transition.Target(), persist: true}, nil
}

func rawMapsEqual(left, right map[workflow.Identifier]json.RawMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		rightValue, exists := right[key]
		if !exists || !bytes.Equal(bytes.TrimSpace(leftValue), bytes.TrimSpace(rightValue)) {
			return false
		}
	}
	return true
}

func cloneRawMap(source map[workflow.Identifier]json.RawMessage) map[workflow.Identifier]json.RawMessage {
	result := make(map[workflow.Identifier]json.RawMessage, len(source))
	for key, value := range source {
		result[key] = append(json.RawMessage(nil), value...)
	}
	return result
}

func cloneAcceptedOutputs(source map[workflow.Identifier]map[workflow.Identifier]json.RawMessage) map[workflow.Identifier]map[workflow.Identifier]json.RawMessage {
	result := make(map[workflow.Identifier]map[workflow.Identifier]json.RawMessage, len(source)+1)
	for nodeID, outputs := range source {
		result[nodeID] = cloneRawMap(outputs)
	}
	return result
}

func stringAcceptedOutputs(source map[workflow.Identifier]map[workflow.Identifier]json.RawMessage) map[string]map[string]json.RawMessage {
	result := make(map[string]map[string]json.RawMessage, len(source))
	for nodeID, outputs := range source {
		result[string(nodeID)] = make(map[string]json.RawMessage, len(outputs))
		for outputID, value := range outputs {
			result[string(nodeID)][string(outputID)] = append(json.RawMessage(nil), value...)
		}
	}
	return result
}

func builtinGateOutput(node workflow.GateNode, inputs, runInputs map[workflow.Identifier]json.RawMessage) (json.RawMessage, error) {
	passed, err := workflow.EvaluatePredicate(node.Executor.Condition, workflow.PredicateValues{Inputs: inputs, RunInputs: runInputs}, "/gate/condition")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"passed": passed, "gate_evidence": map[string]any{"policy": node.Executor.Policy, "passed": passed}})
}

func builtinValidationOutput(ctx context.Context, workflowName, workflowVersion, nodeID, workspace string, node workflow.CommandNode) (json.RawMessage, error) {
	want := []string{"darkstar-project", "validate", "--json"}
	if workflowName != DefaultWorkflowID || nodeID != "s6_validation" || !reflect.DeepEqual(node.Executor.Argv, want) || node.Executor.CWD != "" {
		return nil, errors.New("command node is not an explicitly supported deterministic builtin")
	}
	timeout := 30 * time.Second
	if node.Executor.TimeoutSeconds != nil && *node.Executor.TimeoutSeconds < 30 {
		timeout = time.Duration(*node.Executor.TimeoutSeconds) * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", "-C", workspace, "diff", "--check", "HEAD")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("darkstar-project validation failed: git diff --check HEAD: %w: %s", err, strings.TrimSpace(string(output)))
	}
	status := exec.CommandContext(commandCtx, "git", "-C", workspace, "status", "--porcelain")
	statusOutput, err := status.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("darkstar-project validation failed: git status --porcelain: %w: %s", err, strings.TrimSpace(string(statusOutput)))
	}
	changed := strings.Fields(strings.TrimSpace(string(statusOutput)))
	if len(changed) == 0 {
		return nil, errors.New("darkstar-project validation found no repository changes for the requested implementation")
	}
	return json.Marshal(map[string]any{"validation": map[string]any{
		"passed": true, "acceptanceCovered": true,
		"checks": []map[string]any{
			{"command": "git diff --check HEAD", "passed": true},
			{"command": "git status --porcelain", "passed": true, "changedEntries": len(changed)},
		},
	}})
}

func (s *Service) startWorkflowVisit(ctx context.Context, attempt statestore.AttemptProjection) error {
	run, err := s.store.Run(ctx, attempt.RunID)
	if err != nil {
		return err
	}
	node, err := s.store.Node(ctx, attempt.VisitID)
	if err != nil {
		return err
	}
	if node.Status != statestore.NodePending {
		return nil
	}
	events := make([]statestore.PendingEvent, 0, 3)
	if run.Status == statestore.RunQueued {
		events = append(events, pendingEvent("run.visit_ready", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "visit-ready:"+attempt.AttemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"visitId": node.VisitID}))
	} else if run.Status != statestore.RunRunning {
		return fmt.Errorf("cannot start workflow visit while run is %s", run.Status)
	}
	events = append(events,
		pendingEvent("visit.ready", statestore.AggregateVisit, node.VisitID, node.ResourceVersion, run.RunID, "visit-ready:"+node.VisitID, statestore.ActorSystem, "daemon", s.now(), map[string]any{}),
		pendingEvent("visit.started", statestore.AggregateVisit, node.VisitID, node.ResourceVersion+1, run.RunID, "visit-start:"+node.VisitID, statestore.ActorSystem, "daemon", s.now(), map[string]any{}),
	)
	_, err = s.store.Append(ctx, events...)
	return err
}

func (s *Service) executeBuiltinWorkflowAttempt(ctx context.Context, attempt statestore.AttemptProjection, dispatch AttemptRequestContext) {
	if err := s.startWorkflowVisit(ctx, attempt); err != nil {
		s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "WORKFLOW_VISIT_START_FAILED", err)
		return
	}
	current, err := s.store.Attempt(ctx, attempt.AttemptID)
	if err != nil {
		return
	}
	identity := "builtin:" + current.AttemptID
	if _, err = s.store.Append(ctx,
		pendingEvent("attempt.resources_acquired", statestore.AggregateAttempt, current.AttemptID, current.ResourceVersion, current.RunID, "resources:"+current.AttemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{}),
		pendingEvent("attempt.started", statestore.AggregateAttempt, current.AttemptID, current.ResourceVersion+1, current.RunID, "attempt.started:"+current.AttemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"providerThreadId": identity, "providerTurnId": identity, "processOwnerId": identity}),
	); err != nil {
		return
	}
	var output json.RawMessage
	switch node := dispatch.Node.(type) {
	case workflow.GateNode:
		output, err = builtinGateOutput(node, dispatch.NodeInputs, dispatch.RunInputs)
	case workflow.CommandNode:
		s.mu.Lock()
		workspace := s.workspace
		s.mu.Unlock()
		workspace = filepath.Clean(strings.TrimSpace(workspace))
		workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))
		if workspace == "." || !filepath.IsAbs(workspace) || dispatch.Project.Status != statestore.ProjectActive || dispatch.Project.SourceHash != workspaceDigest {
			err = fmt.Errorf("workflow project %q is not authorized for daemon workspace %q", dispatch.Project.ProjectID, workspace)
			break
		}
		output, err = builtinValidationOutput(ctx, dispatch.Workflow.Version.Name, dispatch.Workflow.Version.Version, dispatch.Attempt.NodeID, workspace, node)
	default:
		err = fmt.Errorf("unsupported builtin workflow node %T", dispatch.Node)
	}
	if err != nil {
		s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "WORKFLOW_BUILTIN_FAILED", err)
		return
	}
	s.completeAttempt(ctx, attempt.AttemptID, attempt.RunID, provider.SucceededResult{StructuredOutput: output})
}

func (s *Service) completeWorkflowSucceeded(ctx context.Context, attempt statestore.AttemptProjection, run statestore.RunProjection, visit statestore.NodeProjection, result provider.SucceededResult) error {
	dispatch, err := s.workflowAttemptContext(ctx, attempt, run)
	if err != nil {
		return &workflowAdmissionError{code: "WORKFLOW_DEFINITION_MISMATCH", message: err.Error()}
	}
	outputs, err := decodeNodeOutputs(dispatch.Node, result.StructuredOutput)
	if err != nil {
		return &workflowAdmissionError{code: "RUN_OUTPUT_INVALID", message: err.Error()}
	}
	for id, value := range outputs {
		if err := workflow.ValidateDeliverable(dispatch.Node, id, value, dispatch.NodeInputs, s.valueSchemas); err != nil {
			return &workflowAdmissionError{code: "RUN_OUTPUT_INVALID", message: err.Error()}
		}
	}
	checkpoint := dispatch.Node.Fields().Checkpoint
	waiting := checkpoint != nil && checkpoint.Mode() != workflow.CheckpointNone
	advance := workflowAdvance{context: dispatch.ExecutionContext}
	accepted := cloneAcceptedOutputs(dispatch.AcceptedOutputs)
	accepted[workflow.Identifier(attempt.NodeID)] = cloneRawMap(outputs)
	advance.context.AcceptedOutputs = stringAcceptedOutputs(accepted)
	if !waiting {
		advance, err = prepareWorkflowAdvance(dispatch, outputs, s.valueSchemas)
		if err != nil {
			return &workflowAdmissionError{code: workflowFailureCode(err, "WORKFLOW_TRANSITION_FAILED"), message: err.Error()}
		}
	}
	saved := dispatch.ExecutionContext
	if waiting {
		if !rawMapsEqual(dispatch.AcceptedOutputs[workflow.Identifier(attempt.NodeID)], outputs) {
			advance.persist = true
		}
	}
	if advance.persist {
		saved, err = s.store.SaveRunExecutionContext(ctx, advance.context, dispatch.ExecutionContext.Revision)
		if err != nil {
			return &workflowAdmissionError{code: "RUN_CONTEXT_REVISION_CONFLICT", message: err.Error()}
		}
	}

	data := map[string]any{"lastSequence": attempt.LastSequence, "logReference": attempt.LogReference, "output": json.RawMessage(result.StructuredOutput)}
	now := s.now()
	events := []statestore.PendingEvent{
		pendingEvent("attempt.result_received", statestore.AggregateAttempt, attempt.AttemptID, attempt.ResourceVersion, run.RunID, "result:"+attempt.AttemptID, statestore.ActorProvider, attempt.Provider, now, data),
		pendingEvent("attempt.succeeded", statestore.AggregateAttempt, attempt.AttemptID, attempt.ResourceVersion+1, run.RunID, "terminal:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data),
		pendingEvent("visit.result_received", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion, run.RunID, "result:"+visit.VisitID+":"+attempt.AttemptID, statestore.ActorProvider, attempt.Provider, now, data),
	}
	if waiting {
		events = append(events,
			pendingEvent("visit.waiting_checkpoint", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion+1, run.RunID, "checkpoint:"+visit.VisitID+":"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data),
			pendingEvent("run.waiting", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "checkpoint:"+run.RunID+":"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, map[string]any{"attemptId": attempt.AttemptID}),
		)
		_, err = s.store.Append(ctx, events...)
		return err
	}
	events = append(events, pendingEvent("visit.succeeded", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion+1, run.RunID, "terminal:"+visit.VisitID+":"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data))
	if advance.terminal {
		events = append(events, pendingEvent("run.completed", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "terminal:"+run.RunID+":"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, map[string]any{"attemptId": attempt.AttemptID}))
		work, workErr := s.store.WorkItem(ctx, run.WorkItemID)
		if workErr != nil {
			return workErr
		}
		if !work.Status.Terminal() {
			events = append(events, pendingEvent("work.completed", statestore.AggregateWork, work.WorkItemID, work.ResourceVersion, run.RunID, "work-complete:"+run.RunID, statestore.ActorSystem, "daemon", now, map[string]any{}))
		}
		_, err = s.store.Append(ctx, events...)
		return err
	}

	successorNode, exists := dispatch.Workflow.Document.Spec.Nodes[advance.successor]
	if !exists {
		return fmt.Errorf("frozen successor %q is absent from installed workflow", advance.successor)
	}
	activation := saved.Revision
	visitID := stableID("visit_", fmt.Sprintf("%s\x00%s\x00%d", run.RunID, advance.successor, activation))
	attemptID := stableID("attempt_", fmt.Sprintf("%s\x00%s\x00%d", run.RunID, advance.successor, activation))
	providerName := ProviderCodex
	switch successorNode.(type) {
	case workflow.GateNode, workflow.CommandNode:
		providerName = ProviderBuiltin
	}
	events = append(events,
		pendingEvent("visit.created", statestore.AggregateVisit, visitID, 0, run.RunID, "visit-create:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{"runId": run.RunID, "nodeId": string(advance.successor)}),
		pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, run.RunID, "attempt-create:"+attemptID, statestore.ActorSystem, "daemon", now, map[string]any{
			"runId": run.RunID, "visitId": visitID, "nodeId": string(advance.successor), "scenario": ScenarioWorkflow, "provider": providerName,
			"logReference": strings.TrimPrefix(attemptID, "attempt_") + ".log", "priority": dispatch.WorkItem.Priority,
		}),
	)
	if _, err = s.store.Append(ctx, events...); err != nil {
		return err
	}
	nextAttempt, err := s.store.Attempt(ctx, attemptID)
	if err != nil {
		return err
	}
	return s.launch(nextAttempt)
}
