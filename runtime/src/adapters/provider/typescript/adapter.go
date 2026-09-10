package typescript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	valueschema "darkstar/src/adapters/valueschema/jsonschema"
	"darkstar/src/ports"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/provider"
)

type EvidenceRecord struct {
	AttemptID       string
	Sequence        uint64
	Kind, MediaType string
	Data            json.RawMessage
}
type Config struct {
	NodeExecutable, Entrypoint, ProviderExecutable, ProjectRoot, Provider string
	Arguments                                                             []string
	Environment                                                           []string
	Ref                                                                   extension.Ref
	RecordEvidence                                                        func(context.Context, EvidenceRecord) (provider.Evidence, error)
}

func (c Config) verify() error {
	if err := c.Ref.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(c.NodeExecutable) || !filepath.IsAbs(c.Entrypoint) || !filepath.IsAbs(c.ProviderExecutable) || c.Provider == "" {
		return errors.New("provider plugin requires host-configured absolute executable paths and identity")
	}
	data, err := os.ReadFile(c.Entrypoint)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != c.Ref.Digest {
		return errors.New("PROVIDER_PLUGIN_DIGEST_MISMATCH")
	}
	return nil
}

type attemptBinding struct {
	handler provider.ToolHandler
	tools   map[string]bool
	schema  json.RawMessage
}
type Adapter struct {
	config   Config
	mu       sync.Mutex
	client   *session
	bindings map[string]attemptBinding
}

func New(config Config) (*Adapter, error) {
	if err := config.verify(); err != nil {
		return nil, err
	}
	config.Arguments = append([]string(nil), config.Arguments...)
	config.Environment = append([]string(nil), config.Environment...)
	return &Adapter{config: config, bindings: map[string]attemptBinding{}}, nil
}
func (a *Adapter) open(ctx context.Context) (*session, error) {
	s, err := startSession(a.config, a.hostCall)
	if err != nil {
		return nil, err
	}
	var d struct {
		Protocol  string                       `json:"protocol"`
		Ref       struct{ ID, Version string } `json:"ref"`
		Providers []struct {
			ID string `json:"id"`
		} `json:"providers"`
	}
	if err = s.call(ctx, "describe", map[string]any{}, &d); err != nil {
		_ = s.Close()
		return nil, err
	}
	if d.Protocol != "darkstar.plugin/v1" || d.Ref.ID != a.config.Ref.ID || d.Ref.Version != a.config.Ref.Version || len(d.Providers) != 1 || d.Providers[0].ID != a.config.Provider {
		_ = s.Close()
		return nil, errors.New("incompatible provider plugin descriptor")
	}
	if err = s.call(ctx, "provider.configure", map[string]any{"Executable": a.config.ProviderExecutable, "Arguments": a.config.Arguments, "Environment": a.config.Environment, "ProjectRoot": a.config.ProjectRoot}, nil); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}
func (a *Adapter) call(ctx context.Context, method string, in, out any) error {
	a.mu.Lock()
	s := a.client
	a.mu.Unlock()
	if s == nil {
		return errors.New("provider plugin attempt has not started")
	}
	return s.call(ctx, method, in, out)
}
func (a *Adapter) isolated(ctx context.Context, method string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = s.Close()
	}()
	return s.call(ctx, method, map[string]any{}, out)
}
func (a *Adapter) ProbeHealth(ctx context.Context) (provider.Health, error) {
	var h provider.Health
	err := a.isolated(ctx, "provider.health", &h)
	if err == nil && h.Provider != a.config.Provider {
		err = errors.New("provider health identity mismatch")
	}
	return h, err
}
func (a *Adapter) Capabilities(ctx context.Context) (provider.CapabilityManifest, error) {
	var wire struct {
		Provider, Fingerprint string
		ObservedAt            time.Time
		Features              map[string]struct {
			Kind, Version, Reason string
			Metadata              map[string]string
		}
	}
	if err := a.isolated(ctx, "provider.capabilities", &wire); err != nil {
		return provider.CapabilityManifest{}, err
	}
	if wire.Provider != a.config.Provider || wire.Fingerprint == "" {
		return provider.CapabilityManifest{}, errors.New("provider capability identity missing")
	}
	result := provider.CapabilityManifest{Provider: wire.Provider, Fingerprint: wire.Fingerprint, ObservedAt: wire.ObservedAt, Features: map[string]provider.Capability{}}
	for id, f := range wire.Features {
		switch f.Kind {
		case "available":
			result.Features[id] = provider.AvailableCapability{Version: f.Version, Metadata: f.Metadata}
		case "unavailable":
			result.Features[id] = provider.UnavailableCapability{Reason: f.Reason}
		default:
			return provider.CapabilityManifest{}, errors.New("invalid provider capability state")
		}
	}
	return result, nil
}
func (a *Adapter) prepare(ctx context.Context, id string, handler provider.ToolHandler, tools []provider.ToolDefinition, schema json.RawMessage) error {
	if id == "" {
		return errors.New("attempt identity is required")
	}
	names := map[string]bool{}
	for _, t := range tools {
		if t.Name == "" || names[t.Name] {
			return errors.New("invalid dynamic tool registration")
		}
		names[t.Name] = true
	}
	a.mu.Lock()
	_, exists := a.bindings[id]
	if !exists {
		a.bindings[id] = attemptBinding{handler: handler, tools: names, schema: append(json.RawMessage(nil), schema...)}
	}
	s := a.client
	a.mu.Unlock()
	if s != nil {
		return nil
	}
	opened, err := a.open(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.client == nil {
		a.client = opened
		opened = nil
	}
	a.mu.Unlock()
	if opened != nil {
		_ = opened.Close()
	}
	return nil
}
func (a *Adapter) StartAttempt(ctx context.Context, r provider.AttemptRequest) (provider.AttemptHandle, error) {
	if err := a.prepare(ctx, r.AttemptID, r.ToolHandler, r.DynamicTools, r.OutputSchema); err != nil {
		return provider.AttemptHandle{}, err
	}
	_, backed := r.ToolHandler.(provider.SubmittedOutputResolver)
	input := struct {
		provider.AttemptRequest
		ToolBackedOutputs bool
	}{r, backed}
	var h provider.AttemptHandle
	err := a.call(ctx, "provider.start", input, &h)
	if err == nil {
		err = a.validateHandle(h, r.AttemptID)
	}
	if err != nil {
		_ = a.Close()
	}
	return h, err
}
func (a *Adapter) ResumeAttempt(ctx context.Context, r provider.ResumeRequest) (provider.AttemptHandle, error) {
	if err := a.prepare(ctx, r.AttemptID, r.ToolHandler, r.DynamicTools, nil); err != nil {
		return provider.AttemptHandle{}, err
	}
	_, backed := r.ToolHandler.(provider.SubmittedOutputResolver)
	input := struct {
		provider.ResumeRequest
		ToolBackedOutputs bool
	}{r, backed}
	var h provider.AttemptHandle
	err := a.call(ctx, "provider.resume", input, &h)
	if err == nil {
		err = a.validateHandle(h, r.AttemptID)
	}
	if err != nil {
		_ = a.Close()
	}
	return h, err
}
func (a *Adapter) validateHandle(h provider.AttemptHandle, id string) error {
	if h.AttemptID != id || h.Provider != a.config.Provider || h.ProviderThreadID == "" || h.ProviderTurnID == "" || h.ProcessOwnerID == "" {
		return errors.New("provider returned an invalid attempt handle")
	}
	return nil
}
func (a *Adapter) Respond(ctx context.Context, r provider.InteractionResponse) (provider.InteractionReceipt, error) {
	var request any
	switch value := r.(type) {
	case provider.PermissionResponse:
		request = struct {
			Kind string
			provider.PermissionResponse
		}{"permission", value}
	case provider.AnswerResponse:
		request = struct {
			Kind string
			provider.AnswerResponse
		}{"answer", value}
	default:
		return provider.InteractionReceipt{}, errors.New("unsupported interaction response")
	}
	var receipt provider.InteractionReceipt
	err := a.call(ctx, "provider.respond", request, &receipt)
	return receipt, err
}
func (a *Adapter) CancelAttempt(ctx context.Context, r provider.CancelRequest) (provider.CancelResult, error) {
	var result provider.CancelResult
	err := a.call(ctx, "provider.cancel", r, &result)
	if err == nil {
		switch result.Disposition {
		case provider.CancelGraceful, provider.CancelForced, provider.CancelAlreadyDone, provider.CancelUncertain:
		default:
			err = errors.New("invalid provider cancellation disposition")
		}
	}
	return result, err
}
func (a *Adapter) GetResult(ctx context.Context, r provider.ResultRequest) (provider.AttemptResult, error) {
	var wire struct {
		Kind string
		provider.AttemptResultMetadata
		StructuredOutput json.RawMessage
		Failure          ports.Failure
	}
	if err := a.call(ctx, "provider.result", r, &wire); err != nil {
		return nil, err
	}
	switch wire.Kind {
	case "succeeded":
		a.mu.Lock()
		binding, ok := a.bindings[r.Handle.AttemptID]
		a.mu.Unlock()
		if !ok {
			return nil, errors.New("unbound provider result")
		}
		output, err := validateOutput(ctx, binding, wire.StructuredOutput)
		if err != nil {
			return nil, err
		}
		return provider.SucceededResult{AttemptResultMetadata: wire.AttemptResultMetadata, StructuredOutput: output}, nil
	case "failed":
		return provider.FailedResult{AttemptResultMetadata: wire.AttemptResultMetadata, Failure: wire.Failure}, nil
	case "interrupted":
		return provider.InterruptedResult{AttemptResultMetadata: wire.AttemptResultMetadata, Failure: wire.Failure}, nil
	case "cancelled":
		return provider.CancelledResult{AttemptResultMetadata: wire.AttemptResultMetadata}, nil
	case "unknown":
		return provider.UnknownResult{AttemptResultMetadata: wire.AttemptResultMetadata, Failure: wire.Failure}, nil
	default:
		return nil, errors.New("invalid provider result state")
	}
}
func (a *Adapter) Close() error {
	a.mu.Lock()
	s := a.client
	a.client = nil
	a.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}

type stream struct {
	adapter  *Adapter
	request  provider.EventRequest
	ctx      context.Context
	cancel   context.CancelFunc
	buffer   []provider.Event
	terminal bool
}

func (a *Adapter) StreamEvents(ctx context.Context, r provider.EventRequest) (provider.EventStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	return &stream{adapter: a, request: r, ctx: ctx, cancel: cancel}, nil
}
func (s *stream) Close() error {
	s.cancel()
	return nil
}
func (s *stream) Receive() (provider.Event, error) {
	for len(s.buffer) == 0 {
		if s.terminal {
			return provider.Event{}, io.EOF
		}
		var batch struct {
			Events   []provider.Event
			Terminal bool
		}
		if err := s.adapter.call(s.ctx, "provider.events", s.request, &batch); err != nil {
			return provider.Event{}, err
		}
		previous := s.request.AfterSequence
		for index, e := range batch.Events {
			if err := e.Validate(); err != nil {
				return provider.Event{}, err
			}
			if e.AttemptID != s.request.Handle.AttemptID || e.Provider != s.adapter.config.Provider || e.Sequence != previous+1 ||
				(e.ProviderThreadID != "" && e.ProviderThreadID != s.request.Handle.ProviderThreadID) ||
				(e.ProviderTurnID != "" && e.ProviderTurnID != s.request.Handle.ProviderTurnID) {
				return provider.Event{}, errors.New("provider event identity or order mismatch")
			}
			if e.Kind == provider.EventStructuredOutputCompleted {
				s.adapter.mu.Lock()
				binding, ok := s.adapter.bindings[e.AttemptID]
				s.adapter.mu.Unlock()
				if !ok {
					return provider.Event{}, errors.New("unbound structured output event")
				}
				output, err := validateOutput(s.ctx, binding, e.Payload)
				if err != nil {
					return provider.Event{}, err
				}
				batch.Events[index].Payload = output
			}
			previous = e.Sequence
		}
		s.buffer = batch.Events
		s.terminal = batch.Terminal
		if len(s.buffer) == 0 && !s.terminal {
			select {
			case <-s.ctx.Done():
				return provider.Event{}, s.ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	e := s.buffer[0]
	s.buffer = s.buffer[1:]
	s.request.AfterSequence = e.Sequence
	return e, nil
}

func validateOutput(ctx context.Context, binding attemptBinding, output json.RawMessage) (json.RawMessage, error) {
	if resolver, ok := binding.handler.(provider.SubmittedOutputResolver); ok {
		value, err := resolver.ResolveSubmittedOutputs(ctx)
		if err != nil {
			return nil, err
		}
		output = value
	}
	if !json.Valid(output) {
		return nil, errors.New("provider output is not JSON")
	}
	// Rejoined attempts have no adapter-local schema; their frozen node contract
	// is validated by the daemon after the provider result is recovered.
	if len(binding.schema) > 0 {
		if err := (valueschema.Validator{}).Validate(binding.schema, output); err != nil {
			return nil, err
		}
	}
	if validator, ok := binding.handler.(interface {
		ValidateFinal(json.RawMessage) error
	}); ok {
		if err := validator.ValidateFinal(output); err != nil {
			return nil, err
		}
	}
	return append(json.RawMessage(nil), output...), nil
}

func (a *Adapter) hostCall(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	var scope struct{ AttemptID string }
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil, err
	}
	a.mu.Lock()
	binding, ok := a.bindings[scope.AttemptID]
	a.mu.Unlock()
	if !ok {
		return nil, errors.New("host callback is not bound to an authorized attempt")
	}
	switch method {
	case "tool.call":
		var args struct {
			AttemptID, CallID, Name string
			Arguments               json.RawMessage
		}
		if err := decodeCallback(raw, &args); err != nil {
			return nil, err
		}
		if args.CallID == "" || !binding.tools[args.Name] || binding.handler == nil {
			return nil, errors.New("provider tool is not granted")
		}
		return binding.handler.Call(ctx, args.CallID, args.Name, args.Arguments)
	case "outputs.resolve":
		var args struct{ AttemptID string }
		if err := decodeCallback(raw, &args); err != nil {
			return nil, err
		}
		resolver, ok := binding.handler.(provider.SubmittedOutputResolver)
		if !ok {
			return nil, errors.New("submitted outputs are unavailable")
		}
		return resolver.ResolveSubmittedOutputs(ctx)
	case "markdown.capture":
		var args struct{ AttemptID, Key string }
		if err := decodeCallback(raw, &args); err != nil {
			return nil, err
		}
		if capture, ok := binding.handler.(interface {
			CaptureMarkdown(context.Context, string) error
		}); ok {
			if err := capture.CaptureMarkdown(ctx, args.Key); err != nil {
				return nil, err
			}
		}
		return json.RawMessage(`{}`), nil
	case "evidence.record":
		var args EvidenceRecord
		if err := decodeCallback(raw, &args); err != nil {
			return nil, err
		}
		if a.config.RecordEvidence == nil {
			return nil, errors.New("provider evidence recorder is unavailable")
		}
		result, err := a.config.RecordEvidence(ctx, args)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	default:
		return nil, errors.New("provider host capability is not granted")
	}
}
func decodeCallback(raw json.RawMessage, value any) error {
	// Schema-free host callbacks still reject unknown authority-bearing fields.
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing callback data")
	}
	return nil
}

var _ provider.Provider = (*Adapter)(nil)
