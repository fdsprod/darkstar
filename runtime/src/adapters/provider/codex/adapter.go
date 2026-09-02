package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fdsprod/darkstar/runtime/src/ports"
	providerport "github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

const providerName = "codex"

// AppServerFactory starts and initializes one owned client process.
type AppServerFactory func(context.Context) (*AppServerClient, InitializeResult, error)

// AdapterOptions configures the concrete Codex provider.
type AdapterOptions struct {
	Executable       string
	Client           AppServerOptions
	Factory          AppServerFactory
	EvidenceRecorder EvidenceRecorder
	Clock            func() time.Time
}

// Adapter implements the provider-neutral port using Codex App Server.
type Adapter struct {
	executable string
	client     AppServerOptions
	factory    AppServerFactory
	evidence   EvidenceRecorder
	clock      func() time.Time

	mu       sync.Mutex
	attempts map[string]*codexAttempt
}

type codexAttempt struct {
	mu      sync.Mutex
	ready   chan struct{}
	changed chan struct{}

	startKey string
	startErr error
	request  providerport.AttemptRequest
	handle   providerport.AttemptHandle
	client   *AppServerClient
	schema   *jsonschema.Schema
	normal   *EventNormalizer

	events            []providerport.Event
	result            providerport.AttemptResult
	terminal          bool
	latestOutput      json.RawMessage
	usage             providerport.Usage
	workspaceEvidence []providerport.Evidence
	terminalEvidence  string
	responses         map[string]attemptResponse
	cancelRequested   bool
}

type attemptResponse struct {
	key     string
	receipt providerport.InteractionReceipt
}

type threadStartParams struct {
	CWD                   string   `json:"cwd"`
	Ephemeral             bool     `json:"ephemeral"`
	Sandbox               string   `json:"sandbox"`
	ApprovalPolicy        string   `json:"approvalPolicy"`
	ApprovalsReviewer     string   `json:"approvalsReviewer,omitempty"`
	ThreadSource          string   `json:"threadSource"`
	Model                 string   `json:"model,omitempty"`
	RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots,omitempty"`
}

type turnStartParams struct {
	ThreadID     string          `json:"threadId"`
	Input        []turnInput     `json:"input"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Model        string          `json:"model,omitempty"`
	Effort       string          `json:"effort,omitempty"`
}

type turnInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

var _ providerport.Provider = (*Adapter)(nil)

// NewAdapter constructs a Codex provider. Evidence persistence is mandatory so
// every surfaced native event can carry a retrievable raw reference.
func NewAdapter(options AdapterOptions) (*Adapter, error) {
	if options.EvidenceRecorder == nil {
		return nil, errors.New("codex adapter evidence recorder is required")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Client.ClientInfo.Name == "" {
		options.Client.ClientInfo = ClientInfo{Name: "darkstar", Title: "DARKSTAR", Version: "0.1.0"}
	}
	if options.Factory == nil {
		if strings.TrimSpace(options.Executable) == "" {
			return nil, errors.New("codex adapter executable is required")
		}
		executable := options.Executable
		clientOptions := options.Client
		options.Factory = func(ctx context.Context) (*AppServerClient, InitializeResult, error) {
			return StartAppServer(ctx, executable, clientOptions)
		}
	}
	return &Adapter{
		executable: options.Executable,
		client:     options.Client,
		factory:    options.Factory,
		evidence:   options.EvidenceRecorder,
		clock:      options.Clock,
		attempts:   make(map[string]*codexAttempt),
	}, nil
}

func (adapter *Adapter) ProbeHealth(ctx context.Context) (providerport.Health, error) {
	if err := ctx.Err(); err != nil {
		return providerport.Health{}, err
	}
	if strings.TrimSpace(adapter.executable) == "" {
		return providerport.Health{State: providerport.HealthAvailable, Provider: providerName, ExecutableIdentity: "injected-app-server", Platform: "test"}, nil
	}
	canonical, err := canonicalExecutable(adapter.executable)
	if err != nil {
		return providerport.Health{State: providerport.HealthUnavailable, Provider: providerName, Diagnostics: []string{err.Error()}}, nil
	}
	command := exec.CommandContext(ctx, canonical, "--version")
	configureAppServerProcess(command)
	output, err := command.Output()
	if err != nil {
		return providerport.Health{State: providerport.HealthUnavailable, Provider: providerName, ExecutableIdentity: canonical, Diagnostics: []string{err.Error()}}, nil
	}
	return providerport.Health{
		State:              providerport.HealthAvailable,
		Provider:           providerName,
		ProviderVersion:    strings.TrimSpace(string(output)),
		ExecutableIdentity: canonical,
		Platform:           "windows",
	}, nil
}

func (adapter *Adapter) Capabilities(ctx context.Context) (providerport.CapabilityManifest, error) {
	if err := ctx.Err(); err != nil {
		return providerport.CapabilityManifest{}, err
	}
	features := map[string]providerport.Capability{
		"app_server":        providerport.AvailableCapability{Version: "v2"},
		"structured_output": providerport.AvailableCapability{Version: "json-schema"},
		"workspace_write":   providerport.AvailableCapability{Version: "sandbox"},
		"interactions":      providerport.AvailableCapability{Version: "json-rpc"},
	}
	fingerprintSource := "app_server=v2|interactions=json-rpc|structured_output=json-schema|workspace_write=sandbox"
	digest := sha256.Sum256([]byte(fingerprintSource))
	return providerport.CapabilityManifest{
		Provider:    providerName,
		Fingerprint: hex.EncodeToString(digest[:]),
		Features:    features,
		ObservedAt:  adapter.clock().UTC(),
	}, nil
}

func (adapter *Adapter) StartAttempt(ctx context.Context, request providerport.AttemptRequest) (providerport.AttemptHandle, error) {
	normalized, schema, err := validateAttemptRequest(request)
	if err != nil {
		return providerport.AttemptHandle{}, err
	}

	adapter.mu.Lock()
	if existing := adapter.attempts[normalized.AttemptID]; existing != nil {
		adapter.mu.Unlock()
		if existing.startKey != normalized.IdempotencyKey {
			return providerport.AttemptHandle{}, adapterFailure(ports.FailureConflict, "attempt ID was already used with a different idempotency key", false)
		}
		return waitForAttemptStart(ctx, existing)
	}
	state := &codexAttempt{
		ready:     make(chan struct{}),
		changed:   make(chan struct{}),
		startKey:  normalized.IdempotencyKey,
		request:   normalized,
		schema:    schema,
		responses: make(map[string]attemptResponse),
	}
	adapter.attempts[normalized.AttemptID] = state
	adapter.mu.Unlock()

	handle, startErr := adapter.startAttempt(ctx, state)
	state.mu.Lock()
	state.handle = handle
	state.startErr = startErr
	close(state.ready)
	state.mu.Unlock()
	if startErr != nil {
		return providerport.AttemptHandle{}, startErr
	}
	go adapter.pump(state)
	return handle, nil
}

func (adapter *Adapter) startAttempt(ctx context.Context, state *codexAttempt) (providerport.AttemptHandle, error) {
	client, _, err := adapter.factory(ctx)
	if err != nil {
		return providerport.AttemptHandle{}, classifyAdapterError(err)
	}
	state.client = client
	normalizer, err := NewEventNormalizer(NormalizerOptions{
		AttemptID:       state.request.AttemptID,
		ProviderVersion: client.ProviderVersion(),
		Clock:           adapter.clock,
	})
	if err != nil {
		_ = client.KillOwnedProcess()
		return providerport.AttemptHandle{}, classifyAdapterError(err)
	}
	state.normal = normalizer

	threadParams := makeThreadStartParams(state.request)
	thread, err := client.StartThread(ctx, threadParams)
	if err != nil {
		_ = client.KillOwnedProcess()
		return providerport.AttemptHandle{}, classifyAdapterError(err)
	}
	turnParams, err := makeTurnStartParams(state.request, thread.ID)
	if err != nil {
		_ = shutdownClient(client)
		return providerport.AttemptHandle{}, err
	}
	turn, err := client.StartTurn(ctx, turnParams)
	if err != nil {
		_ = shutdownClient(client)
		return providerport.AttemptHandle{}, classifyAdapterError(err)
	}
	processID := ""
	if client.ProcessID() != 0 {
		processID = strconv.Itoa(client.ProcessID())
	}
	return providerport.AttemptHandle{
		AttemptID:        state.request.AttemptID,
		Provider:         providerName,
		ProviderThreadID: thread.ID,
		ProviderTurnID:   turn.ID,
		ProcessOwnerID:   processID,
	}, nil
}

func (adapter *Adapter) ResumeAttempt(context.Context, providerport.ResumeRequest) (providerport.AttemptHandle, error) {
	return providerport.AttemptHandle{}, adapterFailure(ports.FailureUnsupported, "Codex thread persistence and resume is implemented by DAR-55", false)
}

func (adapter *Adapter) StreamEvents(ctx context.Context, request providerport.EventRequest) (providerport.EventStream, error) {
	state, err := adapter.attempt(request.Handle.AttemptID)
	if err != nil {
		return nil, err
	}
	if err := validateHandle(state, request.Handle); err != nil {
		return nil, err
	}
	return &attemptEventStream{ctx: ctx, state: state, after: request.AfterSequence, closed: make(chan struct{})}, nil
}

func (adapter *Adapter) Respond(ctx context.Context, response providerport.InteractionResponse) (providerport.InteractionReceipt, error) {
	interaction, payload, err := interactionPayload(response)
	if err != nil {
		return providerport.InteractionReceipt{}, err
	}
	state, err := adapter.attempt(interaction.AttemptID)
	if err != nil {
		return providerport.InteractionReceipt{}, err
	}
	if interaction.ProviderThreadID != state.handle.ProviderThreadID {
		return providerport.InteractionReceipt{}, adapterFailure(ports.FailureInvalidRequest, "interaction thread does not match attempt", false)
	}
	state.mu.Lock()
	if existing, ok := state.responses[interaction.ProviderRequestID]; ok {
		state.mu.Unlock()
		if existing.key == interaction.IdempotencyKey {
			return existing.receipt, nil
		}
		return providerport.InteractionReceipt{}, adapterFailure(ports.FailureConflict, "provider request already has a different response", false)
	}
	state.mu.Unlock()
	requestID := providerRequestID(interaction.ProviderRequestID)
	if err := state.client.Respond(requestID, payload); err != nil {
		return providerport.InteractionReceipt{}, classifyAdapterError(err)
	}
	receipt := providerport.InteractionReceipt{ProviderRequestID: interaction.ProviderRequestID, RecordedAt: adapter.clock().UTC()}
	state.mu.Lock()
	state.responses[interaction.ProviderRequestID] = attemptResponse{key: interaction.IdempotencyKey, receipt: receipt}
	state.mu.Unlock()
	return receipt, nil
}

func (adapter *Adapter) CancelAttempt(ctx context.Context, request providerport.CancelRequest) (providerport.CancelResult, error) {
	state, err := adapter.attempt(request.Handle.AttemptID)
	if err != nil {
		return providerport.CancelResult{}, err
	}
	if err := validateHandle(state, request.Handle); err != nil {
		return providerport.CancelResult{}, err
	}
	state.mu.Lock()
	if state.terminal {
		evidenceRef := state.terminalEvidence
		state.mu.Unlock()
		return providerport.CancelResult{Disposition: providerport.CancelAlreadyDone, EvidenceRef: evidenceRef}, nil
	}
	state.cancelRequested = true
	state.mu.Unlock()
	if err := state.client.InterruptTurn(ctx, state.handle.ProviderThreadID, state.handle.ProviderTurnID); err != nil {
		return providerport.CancelResult{}, classifyAdapterError(err)
	}
	grace := request.GracePeriod
	if grace <= 0 {
		grace = state.request.CancellationGrace
	}
	if grace <= 0 {
		grace = 5 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for {
		state.mu.Lock()
		if state.terminal {
			evidenceRef := state.terminalEvidence
			state.mu.Unlock()
			return providerport.CancelResult{Disposition: providerport.CancelGraceful, EvidenceRef: evidenceRef}, nil
		}
		changed := state.changed
		state.mu.Unlock()
		select {
		case <-ctx.Done():
			return providerport.CancelResult{}, ctx.Err()
		case <-changed:
		case <-timer.C:
			if err := state.client.KillOwnedProcess(); err != nil {
				return providerport.CancelResult{Disposition: providerport.CancelUncertain}, classifyAdapterError(err)
			}
			adapter.complete(state, providerport.CancelledResult{AttemptResultMetadata: adapter.metadata(state)})
			return providerport.CancelResult{Disposition: providerport.CancelForced}, nil
		}
	}
}

func (adapter *Adapter) GetResult(ctx context.Context, request providerport.ResultRequest) (providerport.AttemptResult, error) {
	state, err := adapter.attempt(request.Handle.AttemptID)
	if err != nil {
		return nil, err
	}
	if err := validateHandle(state, request.Handle); err != nil {
		return nil, err
	}
	for {
		state.mu.Lock()
		if state.terminal {
			result := cloneAttemptResult(state.result)
			state.mu.Unlock()
			return result, nil
		}
		changed := state.changed
		state.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (adapter *Adapter) pump(state *codexAttempt) {
	for message := range state.client.Messages() {
		event, err := state.normal.Normalize(message)
		if err != nil {
			adapter.failPump(state, ports.FailureProtocolDrift, "Codex event normalization failed", err)
			return
		}
		record, err := nativeEvidenceRecord(state.request.AttemptID, event.Sequence, message)
		if err != nil {
			adapter.failPump(state, ports.FailureProtocolDrift, "Codex event evidence encoding failed", err)
			return
		}
		evidence, err := adapter.evidence.Record(context.Background(), record)
		if err != nil {
			adapter.failPump(state, ports.FailureInternal, "Codex event evidence could not be persisted", err)
			return
		}
		event.RawEvidenceRef = evidence.Ref
		adapter.appendEvent(state, event, evidence)
		if terminal := adapter.observeEvent(state, event); terminal {
			return
		}
	}
	state.mu.Lock()
	terminal := state.terminal
	state.mu.Unlock()
	if !terminal {
		adapter.failPump(state, ports.FailureUncertain, "Codex App Server closed before a terminal turn event", io.EOF)
	}
}

func (adapter *Adapter) observeEvent(state *codexAttempt, event providerport.Event) bool {
	params := eventParams(event.Payload)
	switch event.Kind {
	case providerport.EventUsageUpdated:
		if tokenUsage, ok := rawObject(params["tokenUsage"]); ok {
			if total, ok := rawObject(tokenUsage["total"]); ok {
				state.mu.Lock()
				state.usage = providerport.Usage{
					InputTokens:  rawInt64(total["inputTokens"]),
					CachedTokens: rawInt64(total["cachedInputTokens"]),
					OutputTokens: rawInt64(total["outputTokens"]),
				}
				state.mu.Unlock()
			}
		}
	case providerport.EventMessageCompleted:
		if item, ok := rawObject(params["item"]); ok {
			phase := rawString(item["phase"])
			if phase != "" && phase != "final_answer" {
				break
			}
			text := rawString(item["text"])
			state.mu.Lock()
			state.latestOutput = json.RawMessage(text)
			state.mu.Unlock()
		}
	case providerport.EventTurnInterrupted:
		adapter.finishInterrupted(state)
		return true
	case providerport.EventTurnCompleted:
		adapter.finishTurn(state, params)
		return true
	}
	return false
}

func (adapter *Adapter) finishTurn(state *codexAttempt, params map[string]json.RawMessage) {
	turn, _ := rawObject(params["turn"])
	status := rawString(turn["status"])
	if status != "completed" {
		failure := ports.Failure{Code: ports.FailureInternal, Message: "Codex turn did not complete successfully", Details: map[string]string{"status": status}}
		_ = shutdownClient(state.client)
		adapter.complete(state, providerport.FailedResult{AttemptResultMetadata: adapter.metadata(state), Failure: failure})
		return
	}
	state.mu.Lock()
	output := cloneRaw(state.latestOutput)
	state.mu.Unlock()
	if len(bytes.TrimSpace(output)) == 0 || !json.Valid(output) {
		failure := ports.Failure{Code: ports.FailureInvalidRequest, Message: "Codex structured output is not valid JSON", Details: map[string]string{"phase": "output_validation"}}
		_ = shutdownClient(state.client)
		adapter.complete(state, providerport.FailedResult{AttemptResultMetadata: adapter.metadata(state), Failure: failure})
		return
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(output))
	if err != nil || state.schema.Validate(instance) != nil {
		failure := ports.Failure{Code: ports.FailureInvalidRequest, Message: "Codex structured output does not match the requested schema", Details: map[string]string{"phase": "output_validation"}}
		_ = shutdownClient(state.client)
		adapter.complete(state, providerport.FailedResult{AttemptResultMetadata: adapter.metadata(state), Failure: failure})
		return
	}
	derived, err := state.normal.Emit(providerport.EventStructuredOutputCompleted, output, state.handle.ProviderThreadID, state.handle.ProviderTurnID, "structured-output")
	if err != nil {
		adapter.failPump(state, ports.FailureInternal, "structured output event could not be created", err)
		return
	}
	evidence, err := adapter.evidence.Record(context.Background(), EvidenceRecord{
		AttemptID: state.request.AttemptID,
		Sequence:  derived.Sequence,
		Kind:      string(derived.Kind),
		MediaType: "application/json",
		Data:      output,
	})
	if err != nil {
		adapter.failPump(state, ports.FailureInternal, "structured output evidence could not be persisted", err)
		return
	}
	derived.RawEvidenceRef = evidence.Ref
	adapter.appendEvent(state, derived, evidence)
	if err := shutdownClient(state.client); err != nil {
		failure := ports.Failure{Code: ports.FailureUncertain, Message: "Codex thread completed but ownership release failed", Details: map[string]string{"shutdown": err.Error()}}
		adapter.complete(state, providerport.UnknownResult{AttemptResultMetadata: adapter.metadata(state), Failure: failure})
		return
	}
	adapter.complete(state, providerport.SucceededResult{AttemptResultMetadata: adapter.metadata(state), StructuredOutput: output})
}

func (adapter *Adapter) finishInterrupted(state *codexAttempt) {
	_ = shutdownClient(state.client)
	state.mu.Lock()
	cancelled := state.cancelRequested
	state.mu.Unlock()
	if cancelled {
		adapter.complete(state, providerport.CancelledResult{AttemptResultMetadata: adapter.metadata(state)})
		return
	}
	failure := ports.Failure{Code: ports.FailureInterrupted, Message: "Codex turn was interrupted", Retryable: true}
	adapter.complete(state, providerport.InterruptedResult{AttemptResultMetadata: adapter.metadata(state), Failure: failure})
}

func (adapter *Adapter) appendEvent(state *codexAttempt, event providerport.Event, evidence providerport.Evidence) {
	state.mu.Lock()
	state.events = append(state.events, event)
	if event.Kind == providerport.EventCommandCompleted || event.Kind == providerport.EventFileChangeStarted || event.Kind == providerport.EventFileChangeCompleted || event.Kind == providerport.EventStructuredOutputCompleted {
		state.workspaceEvidence = append(state.workspaceEvidence, evidence)
	}
	state.terminalEvidence = evidence.Ref
	signalAttempt(state)
	state.mu.Unlock()
}

func (adapter *Adapter) complete(state *codexAttempt, result providerport.AttemptResult) {
	state.mu.Lock()
	if !state.terminal {
		state.result = result
		state.terminal = true
		signalAttempt(state)
	}
	state.mu.Unlock()
}

func (adapter *Adapter) failPump(state *codexAttempt, code ports.FailureCode, message string, cause error) {
	_ = shutdownClient(state.client)
	failure := ports.Failure{Code: code, Message: message, Details: map[string]string{"cause": cause.Error()}}
	adapter.complete(state, providerport.FailedResult{AttemptResultMetadata: adapter.metadata(state), Failure: failure})
}

func (adapter *Adapter) metadata(state *codexAttempt) providerport.AttemptResultMetadata {
	state.mu.Lock()
	defer state.mu.Unlock()
	lastSequence := uint64(0)
	if len(state.events) > 0 {
		lastSequence = state.events[len(state.events)-1].Sequence
	}
	return providerport.AttemptResultMetadata{
		Usage:             state.usage,
		WorkspaceEvidence: append([]providerport.Evidence(nil), state.workspaceEvidence...),
		Recovery: providerport.RecoveryMetadata{
			ProviderThreadID: state.handle.ProviderThreadID,
			ProviderTurnID:   state.handle.ProviderTurnID,
			LastSequence:     lastSequence,
			ProcessOwnerID:   state.handle.ProcessOwnerID,
			Resumable:        state.handle.ProviderThreadID != "",
			EvidenceRef:      state.terminalEvidence,
		},
	}
}

func (adapter *Adapter) attempt(attemptID string) (*codexAttempt, error) {
	adapter.mu.Lock()
	state := adapter.attempts[attemptID]
	adapter.mu.Unlock()
	if state == nil {
		return nil, adapterFailure(ports.FailureNotFound, "Codex attempt was not found", false)
	}
	select {
	case <-state.ready:
		if state.startErr != nil {
			return nil, state.startErr
		}
		return state, nil
	default:
		return nil, adapterFailure(ports.FailureConflict, "Codex attempt is still starting", true)
	}
}

type attemptEventStream struct {
	ctx    context.Context
	state  *codexAttempt
	after  uint64
	closed chan struct{}
	once   sync.Once
}

func (stream *attemptEventStream) Receive() (providerport.Event, error) {
	for {
		stream.state.mu.Lock()
		for _, event := range stream.state.events {
			if event.Sequence > stream.after {
				stream.after = event.Sequence
				cloned := cloneEvent(event)
				stream.state.mu.Unlock()
				return cloned, nil
			}
		}
		terminal := stream.state.terminal
		changed := stream.state.changed
		stream.state.mu.Unlock()
		if terminal {
			return providerport.Event{}, io.EOF
		}
		select {
		case <-stream.ctx.Done():
			return providerport.Event{}, stream.ctx.Err()
		case <-stream.closed:
			return providerport.Event{}, io.EOF
		case <-changed:
		}
	}
}

func (stream *attemptEventStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return nil
}

func validateAttemptRequest(request providerport.AttemptRequest) (providerport.AttemptRequest, *jsonschema.Schema, error) {
	switch {
	case strings.TrimSpace(request.AttemptID) == "":
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "attempt ID is required", false)
	case strings.TrimSpace(request.RunID) == "":
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "run ID is required", false)
	case strings.TrimSpace(request.NodeID) == "":
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "node ID is required", false)
	case strings.TrimSpace(request.IdempotencyKey) == "":
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "idempotency key is required", false)
	case strings.TrimSpace(request.Prompt) == "":
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "structured node prompt is required", false)
	case request.Access != providerport.AccessReadOnly && request.Access != providerport.AccessWorkspaceWrite:
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "unsupported Codex access class", false)
	case request.Network != providerport.NetworkDenied:
		return request, nil, adapterFailure(ports.FailureUnsupported, "Codex structured nodes currently require denied network policy", false)
	case !validInteractionPolicy(request.CommandPolicy) || !validInteractionPolicy(request.FilePolicy) || !validInteractionPolicy(request.ToolPolicy):
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "unsupported Codex interaction policy", false)
	case len(bytes.TrimSpace(request.OutputSchema)) == 0:
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "structured node output schema is required", false)
	}
	workspace, err := canonicalDirectory(request.Workspace)
	if err != nil {
		return request, nil, adapterFailure(ports.FailureInvalidRequest, err.Error(), false)
	}
	request.Workspace = workspace
	request.AdditionalRoots = append([]string(nil), request.AdditionalRoots...)
	for index, root := range request.AdditionalRoots {
		canonical, err := canonicalDirectory(root)
		if err != nil {
			return request, nil, adapterFailure(ports.FailureInvalidRequest, err.Error(), false)
		}
		request.AdditionalRoots[index] = canonical
	}
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(request.OutputSchema))
	if err != nil {
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "output schema is not valid JSON", false)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	if err := compiler.AddResource("darkstar-output.json", schemaDocument); err != nil {
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "output schema could not be loaded", false)
	}
	compiled, err := compiler.Compile("darkstar-output.json")
	if err != nil {
		return request, nil, adapterFailure(ports.FailureInvalidRequest, "output schema could not be compiled", false)
	}
	for _, input := range request.Inputs {
		if input.Kind != providerport.InputText && input.Kind != providerport.InputArtifact {
			return request, nil, adapterFailure(ports.FailureUnsupported, "image and skill inputs are implemented by DAR-61", false)
		}
		if strings.TrimSpace(input.Text) == "" {
			return request, nil, adapterFailure(ports.FailureInvalidRequest, "text and artifact inputs require prepared text", false)
		}
	}
	return request, compiled, nil
}

func validInteractionPolicy(policy providerport.InteractionPolicy) bool {
	return policy == providerport.InteractionDeny || policy == providerport.InteractionAsk || policy == providerport.InteractionAllow
}

func makeThreadStartParams(request providerport.AttemptRequest) threadStartParams {
	sandbox := "read-only"
	if request.Access == providerport.AccessWorkspaceWrite {
		sandbox = "workspace-write"
	}
	approvalPolicy := "never"
	if request.CommandPolicy == providerport.InteractionAsk || request.FilePolicy == providerport.InteractionAsk || request.ToolPolicy == providerport.InteractionAsk {
		approvalPolicy = "on-request"
	}
	roots := append([]string{request.Workspace}, request.AdditionalRoots...)
	sort.Strings(roots)
	params := threadStartParams{
		CWD:                   request.Workspace,
		Ephemeral:             false,
		Sandbox:               sandbox,
		ApprovalPolicy:        approvalPolicy,
		ThreadSource:          "darkstar",
		Model:                 request.ModelHint,
		RuntimeWorkspaceRoots: roots,
	}
	if approvalPolicy == "on-request" {
		params.ApprovalsReviewer = "user"
	}
	return params
}

func makeTurnStartParams(request providerport.AttemptRequest, threadID string) (turnStartParams, error) {
	inputs := []turnInput{{Type: "text", Text: request.Prompt}}
	for _, input := range request.Inputs {
		inputs = append(inputs, turnInput{Type: "text", Text: input.Text})
	}
	return turnStartParams{
		ThreadID:     threadID,
		Input:        inputs,
		OutputSchema: cloneRaw(request.OutputSchema),
		Model:        request.ModelHint,
		Effort:       request.ReasoningHint,
	}, nil
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("workspace directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace directory: %w", err)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
		absolute = evaluated
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workspace directory %q does not exist", absolute)
	}
	return filepath.Clean(absolute), nil
}

func waitForAttemptStart(ctx context.Context, state *codexAttempt) (providerport.AttemptHandle, error) {
	select {
	case <-ctx.Done():
		return providerport.AttemptHandle{}, ctx.Err()
	case <-state.ready:
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.handle, state.startErr
	}
}

func validateHandle(state *codexAttempt, handle providerport.AttemptHandle) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if handle != state.handle {
		return adapterFailure(ports.FailureInvalidRequest, "provider attempt handle does not match", false)
	}
	return nil
}

func interactionPayload(response providerport.InteractionResponse) (providerport.InteractionContext, any, error) {
	switch response := response.(type) {
	case providerport.PermissionResponse:
		var decision string
		switch response.Decision {
		case providerport.PermissionAllowOnce:
			decision = "accept"
		case providerport.PermissionAllowForSession:
			return response.InteractionContext, nil, adapterFailure(ports.FailureUnsupported, "Codex App Server does not expose a portable session grant response", false)
		case providerport.PermissionDenied, providerport.PermissionCancelled, providerport.PermissionExpired:
			decision = "cancel"
		default:
			return response.InteractionContext, nil, adapterFailure(ports.FailureInvalidRequest, "unsupported permission decision", false)
		}
		return response.InteractionContext, map[string]string{"decision": decision}, nil
	case providerport.AnswerResponse:
		if len(bytes.TrimSpace(response.Answer)) == 0 || !json.Valid(response.Answer) {
			return response.InteractionContext, nil, adapterFailure(ports.FailureInvalidRequest, "interaction answer must be valid JSON", false)
		}
		return response.InteractionContext, cloneRaw(response.Answer), nil
	default:
		return providerport.InteractionContext{}, nil, adapterFailure(ports.FailureInvalidRequest, fmt.Sprintf("unsupported interaction response %T", response), false)
	}
}

func providerRequestID(value string) json.RawMessage {
	trimmed := strings.TrimSpace(value)
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	payload, _ := json.Marshal(value)
	return payload
}

func nativeEvidenceRecord(attemptID string, sequence uint64, message IncomingMessage) (EvidenceRecord, error) {
	var method string
	var value any
	kind := "provider_notification"
	switch message := message.(type) {
	case ServerNotification:
		method = message.Method
		value = struct {
			Method      string          `json:"method"`
			Params      json.RawMessage `json:"params"`
			EmittedAtMS int64           `json:"emittedAtMs,omitempty"`
		}{message.Method, message.Params, message.EmittedAtMS}
	case ServerRequest:
		method = message.Method
		kind = "provider_request"
		value = struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}{message.ID, message.Method, message.Params}
	default:
		return EvidenceRecord{}, fmt.Errorf("unsupported evidence message %T", message)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return EvidenceRecord{}, err
	}
	return EvidenceRecord{AttemptID: attemptID, Sequence: sequence, Kind: kind + "_" + safePathPart(method), MediaType: "application/json", Data: payload}, nil
}

func eventParams(payload json.RawMessage) map[string]json.RawMessage {
	outer, ok := rawObject(payload)
	if !ok {
		return map[string]json.RawMessage{}
	}
	params, ok := rawObject(outer["params"])
	if !ok {
		return map[string]json.RawMessage{}
	}
	return params
}

func rawInt64(raw json.RawMessage) int64 {
	var value int64
	_ = json.Unmarshal(raw, &value)
	return value
}

func signalAttempt(state *codexAttempt) {
	close(state.changed)
	state.changed = make(chan struct{})
}

func shutdownClient(client *AppServerClient) error {
	if client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	return client.Shutdown(ctx)
}

func classifyAdapterError(err error) error {
	if err == nil {
		return nil
	}
	var rpcError *RPCError
	switch {
	case errors.As(err, &rpcError):
		message := strings.ToLower(rpcError.Message)
		switch {
		case strings.Contains(message, "auth") || strings.Contains(message, "login"):
			return adapterFailure(ports.FailureUnauthenticated, "Codex authentication is required", false)
		case strings.Contains(message, "rate") || strings.Contains(message, "usage"):
			return adapterFailure(ports.FailureResourceExhausted, "Codex usage or rate limit was reached", true)
		case strings.Contains(message, "active writer"):
			return adapterFailure(ports.FailureUncertain, "Codex thread already has an active writer", false)
		default:
			return adapterFailure(ports.FailureInternal, "Codex App Server request failed", false)
		}
	case errors.Is(err, context.DeadlineExceeded):
		return adapterFailure(ports.FailureTimeout, "Codex operation timed out", true)
	case errors.Is(err, context.Canceled):
		return adapterFailure(ports.FailureCancelled, "Codex operation was cancelled", false)
	default:
		return adapterFailure(ports.FailureInternal, err.Error(), false)
	}
}

func adapterFailure(code ports.FailureCode, message string, retryable bool) *ports.Failure {
	return &ports.Failure{Code: code, Message: message, Retryable: retryable}
}

func cloneEvent(event providerport.Event) providerport.Event {
	event.Payload = cloneRaw(event.Payload)
	return event
}

func cloneAttemptResult(result providerport.AttemptResult) providerport.AttemptResult {
	switch result := result.(type) {
	case providerport.SucceededResult:
		result.StructuredOutput = cloneRaw(result.StructuredOutput)
		result.AttemptResultMetadata = cloneMetadata(result.AttemptResultMetadata)
		return result
	case providerport.FailedResult:
		result.AttemptResultMetadata = cloneMetadata(result.AttemptResultMetadata)
		result.Failure = cloneFailureValue(result.Failure)
		return result
	case providerport.InterruptedResult:
		result.AttemptResultMetadata = cloneMetadata(result.AttemptResultMetadata)
		result.Failure = cloneFailureValue(result.Failure)
		return result
	case providerport.CancelledResult:
		result.AttemptResultMetadata = cloneMetadata(result.AttemptResultMetadata)
		return result
	case providerport.UnknownResult:
		result.AttemptResultMetadata = cloneMetadata(result.AttemptResultMetadata)
		result.Failure = cloneFailureValue(result.Failure)
		return result
	default:
		return result
	}
}

func cloneMetadata(metadata providerport.AttemptResultMetadata) providerport.AttemptResultMetadata {
	metadata.WorkspaceEvidence = append([]providerport.Evidence(nil), metadata.WorkspaceEvidence...)
	return metadata
}

func cloneFailureValue(failure ports.Failure) ports.Failure {
	failure.Details = cloneStringMap(failure.Details)
	return failure
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
