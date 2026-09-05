// Package configmutation owns typed, audited configuration changes.
package configmutation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"darkstar/src/core/config"
	"darkstar/src/core/identity"
	"darkstar/src/ports/configurationstore"
	"darkstar/src/ports/statestore"
)

var (
	ErrInvalidRequest    = errors.New("invalid configuration request")
	ErrProjectNotFound   = errors.New("configuration project not found")
	ErrProjectMismatch   = errors.New("configuration project does not match daemon project")
	ErrCommandInProgress = errors.New("configuration command is still being recovered")
)

type ChangeOperation string

const (
	ChangeSet   ChangeOperation = "set"
	ChangeUnset ChangeOperation = "unset"
)

// SettingChange is a discriminated union; unset cannot accidentally carry a value.
type SettingChange struct{ variant changeVariant }
type changeVariant interface {
	operation() ChangeOperation
	value() (config.TypedValue, bool)
}
type unsetChange struct{}
type setChange struct{ configured config.TypedValue }

func (unsetChange) operation() ChangeOperation       { return ChangeUnset }
func (unsetChange) value() (config.TypedValue, bool) { return config.TypedValue{}, false }
func (setChange) operation() ChangeOperation         { return ChangeSet }
func (v setChange) value() (config.TypedValue, bool) { return v.configured, true }
func Set(value config.TypedValue) SettingChange {
	return SettingChange{variant: setChange{configured: value}}
}
func Unset() SettingChange { return SettingChange{variant: unsetChange{}} }
func (c SettingChange) Operation() ChangeOperation {
	if c.variant == nil {
		return ""
	}
	return c.variant.operation()
}
func (c SettingChange) Value() (config.TypedValue, bool) {
	if c.variant == nil {
		return config.TypedValue{}, false
	}
	return c.variant.value()
}
func (c SettingChange) MarshalJSON() ([]byte, error) {
	if value, ok := c.Value(); ok {
		return json.Marshal(struct {
			Operation ChangeOperation   `json:"operation"`
			Value     config.TypedValue `json:"value"`
		}{ChangeSet, value})
	}
	if c.Operation() == ChangeUnset {
		return []byte(`{"operation":"unset"}`), nil
	}
	return nil, errors.New("setting change is required")
}
func (c *SettingChange) UnmarshalJSON(encoded []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return err
	}
	var operation ChangeOperation
	if err := json.Unmarshal(raw["operation"], &operation); err != nil {
		return errors.New("change operation is required")
	}
	switch operation {
	case ChangeSet:
		if len(raw) != 2 || raw["value"] == nil {
			return errors.New("set change requires only operation and value")
		}
		var value config.TypedValue
		if err := json.Unmarshal(raw["value"], &value); err != nil {
			return err
		}
		*c = Set(value)
	case ChangeUnset:
		if len(raw) != 1 {
			return errors.New("unset change accepts only operation")
		}
		*c = Unset()
	default:
		return fmt.Errorf("unsupported change operation %q", operation)
	}
	return nil
}

type MutationRequest struct {
	Scope            config.MutationScope `json:"scope"`
	Key              string               `json:"key"`
	Change           SettingChange        `json:"change"`
	ExpectedRevision string               `json:"expectedRevision"`
}
type ApplyRequest struct {
	MutationRequest
	IdempotencyKey string `json:"idempotencyKey"`
}
type RestoreRequest struct {
	Scope            config.MutationScope `json:"scope"`
	ExpectedRevision string               `json:"expectedRevision"`
	IdempotencyKey   string               `json:"idempotencyKey"`
}
type SecretWriteRequest struct {
	Name             string `json:"name"`
	Value            string `json:"value"`
	ExpectedRevision string `json:"expectedRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type ScopedSetting struct {
	Key   string            `json:"key"`
	Value config.TypedValue `json:"value"`
}
type EffectiveSetting struct {
	Key    string            `json:"key"`
	Value  config.TypedValue `json:"value"`
	Source config.Source     `json:"source"`
}
type State struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	Scope          config.MutationScope `json:"scope"`
	Revision       string               `json:"revision"`
	SecretRevision string               `json:"secretRevision,omitempty"`
	Configured     []ScopedSetting      `json:"configured"`
	Effective      []EffectiveSetting   `json:"effective"`
}
type ValidationIssue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Preview struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Valid         bool                 `json:"valid"`
	Before        State                `json:"before"`
	After         State                `json:"after"`
	Issues        []ValidationIssue    `json:"issues"`
	Restart       config.RestartImpact `json:"restart"`
}
type ApplyResult struct {
	SchemaVersion int                  `json:"schemaVersion"`
	State         State                `json:"state"`
	Restart       config.RestartImpact `json:"restart"`
	Replayed      bool                 `json:"replayed"`
}
type SecretReceipt struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Name          string               `json:"name"`
	Revision      string               `json:"revision"`
	Restart       config.RestartImpact `json:"restart"`
	Replayed      bool                 `json:"replayed"`
}

type AuditStore interface {
	Append(context.Context, ...statestore.PendingEvent) ([]statestore.Event, error)
	BeginCommand(context.Context, statestore.BeginCommandRequest) (statestore.CommandEvidence, bool, error)
	CompleteCommand(context.Context, statestore.CompleteCommandRequest) (statestore.CommandEvidence, error)
	Project(context.Context, string) (statestore.ProjectProjection, error)
}

type Service struct {
	files       configurationstore.Store
	audit       AuditStore
	projectRoot string
	now         func() time.Time
}

func New(files configurationstore.Store, audit AuditStore, projectRoot string) (*Service, error) {
	if files == nil || audit == nil {
		return nil, errors.New("configuration mutation requires file and audit stores")
	}
	if !filepath.IsAbs(projectRoot) {
		return nil, errors.New("configuration mutation project root must be absolute")
	}
	return &Service{files: files, audit: audit, projectRoot: filepath.Clean(projectRoot), now: time.Now}, nil
}

func (s *Service) Catalog(context.Context) (config.Catalog, error) {
	return config.SupportedCatalog(), nil
}

func (s *Service) State(ctx context.Context, scope config.MutationScope) (State, error) {
	target, err := s.authorize(ctx, scope)
	if err != nil {
		return State{}, err
	}
	return s.state(ctx, scope, target, nil)
}

func (s *Service) Preview(ctx context.Context, request MutationRequest) (Preview, error) {
	target, descriptor, mutation, err := s.validate(ctx, request)
	if err != nil {
		return Preview{}, err
	}
	before, err := s.state(ctx, request.Scope, target, nil)
	if err != nil {
		return Preview{}, err
	}
	candidate, err := s.files.Preview(ctx, target, mutation)
	if err != nil {
		return Preview{}, err
	}
	after, err := s.state(ctx, request.Scope, target, &candidate)
	if err != nil {
		return Preview{}, err
	}
	return Preview{SchemaVersion: 1, Valid: true, Before: before, After: after, Issues: []ValidationIssue{}, Restart: descriptor.Restart}, nil
}

func (s *Service) Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	target, descriptor, mutation, err := s.validate(ctx, request.MutationRequest)
	if err != nil {
		s.rejected(ctx, request.IdempotencyKey, request.Scope, request.Key, "validation_failed")
		return ApplyResult{}, err
	}
	command, reused, err := s.begin(ctx, "configuration.apply", request.IdempotencyKey, request)
	if err != nil {
		return ApplyResult{}, err
	}
	if reused && command.Status == "completed" {
		if command.ResponseStatus != nil && *command.ResponseStatus != 200 {
			return ApplyResult{}, replayFailure(command.Response)
		}
		var result ApplyResult
		if err := json.Unmarshal(command.Response, &result); err != nil {
			return ApplyResult{}, err
		}
		result.Replayed = true
		return result, nil
	}
	if reused {
		current, readErr := s.files.Snapshot(ctx, target)
		if readErr != nil {
			return ApplyResult{}, readErr
		}
		if current.Revision == request.ExpectedRevision {
			current, readErr = s.files.Apply(ctx, target, mutation, request.ExpectedRevision)
			if readErr != nil {
				return ApplyResult{}, readErr
			}
			return s.finishApply(ctx, request, descriptor, target, current, true, command.CreatedAt)
		}
		candidate, previewErr := s.files.Preview(ctx, target, mutation)
		if previewErr != nil || candidate.Revision != current.Revision {
			return ApplyResult{}, ErrCommandInProgress
		}
		return s.finishApply(ctx, request, descriptor, target, current, true, command.CreatedAt)
	}
	snapshot, err := s.files.Apply(ctx, target, mutation, request.ExpectedRevision)
	if err != nil {
		failure := classify(err)
		_ = s.recordRejected(ctx, "configuration.apply", request.IdempotencyKey, request.Scope, request.Key, failure, command.CreatedAt, err)
		return ApplyResult{}, err
	}
	return s.finishApply(ctx, request, descriptor, target, snapshot, false, command.CreatedAt)
}

func (s *Service) Restore(ctx context.Context, request RestoreRequest) (ApplyResult, error) {
	target, err := s.authorize(ctx, request.Scope)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := validateRevision(request.ExpectedRevision); err != nil {
		return ApplyResult{}, err
	}
	command, reused, err := s.begin(ctx, "configuration.restore", request.IdempotencyKey, request)
	if err != nil {
		return ApplyResult{}, err
	}
	if reused && command.Status == "completed" {
		if command.ResponseStatus != nil && *command.ResponseStatus != 200 {
			return ApplyResult{}, replayFailure(command.Response)
		}
		var result ApplyResult
		if err := json.Unmarshal(command.Response, &result); err != nil {
			return ApplyResult{}, err
		}
		result.Replayed = true
		return result, nil
	}
	if reused {
		current, readErr := s.files.Snapshot(ctx, target)
		if readErr != nil {
			return ApplyResult{}, readErr
		}
		if current.Revision == request.ExpectedRevision {
			current, readErr = s.files.Restore(ctx, target, request.ExpectedRevision)
			if readErr != nil {
				return ApplyResult{}, readErr
			}
		}
		result := ApplyResult{SchemaVersion: 1, Restart: config.RestartDaemon, Replayed: true}
		result.State, readErr = s.state(ctx, request.Scope, target, &current)
		if readErr != nil {
			return ApplyResult{}, readErr
		}
		return result, s.recordAndComplete(ctx, "configuration.restore", request.IdempotencyKey, request.Scope, "", request.ExpectedRevision, current.Revision, command.CreatedAt, result)
	}
	snapshot, err := s.files.Restore(ctx, target, request.ExpectedRevision)
	if err != nil {
		_ = s.recordRejected(ctx, "configuration.restore", request.IdempotencyKey, request.Scope, "", classify(err), command.CreatedAt, err)
		return ApplyResult{}, err
	}
	result := ApplyResult{SchemaVersion: 1, Restart: config.RestartDaemon}
	result.State, err = s.state(ctx, request.Scope, target, &snapshot)
	if err != nil {
		return ApplyResult{}, err
	}
	return result, s.recordAndComplete(ctx, "configuration.restore", request.IdempotencyKey, request.Scope, "", request.ExpectedRevision, snapshot.Revision, command.CreatedAt, result)
}

func (s *Service) WriteSecret(ctx context.Context, request SecretWriteRequest) (SecretReceipt, error) {
	request.Name = strings.TrimSpace(request.Name)
	if !secretNamePattern.MatchString(request.Name) || request.Value == "" {
		return SecretReceipt{}, fmt.Errorf("%w: secret name and value are required", ErrInvalidRequest)
	}
	if err := validateRevision(request.ExpectedRevision); err != nil {
		return SecretReceipt{}, err
	}
	commandRequest := request
	commandRequest.Value = digest(request.Value)
	command, reused, err := s.begin(ctx, "configuration.secret", request.IdempotencyKey, commandRequest)
	if err != nil {
		return SecretReceipt{}, err
	}
	if reused && command.Status == "completed" {
		if command.ResponseStatus != nil && *command.ResponseStatus != 200 {
			return SecretReceipt{}, replayFailure(command.Response)
		}
		var result SecretReceipt
		if err := json.Unmarshal(command.Response, &result); err != nil {
			return SecretReceipt{}, err
		}
		result.Replayed = true
		return result, nil
	}
	if reused {
		current, readErr := s.files.SecretRevision(ctx)
		if readErr != nil {
			return SecretReceipt{}, readErr
		}
		if current == request.ExpectedRevision {
			written, writeErr := s.files.PutSecret(ctx, request.Name, request.Value, request.ExpectedRevision)
			if writeErr != nil {
				return SecretReceipt{}, writeErr
			}
			current = written.Revision
		}
		result := SecretReceipt{SchemaVersion: 1, Name: request.Name, Revision: current, Restart: config.RestartDaemon, Replayed: true}
		return result, s.recordAndComplete(ctx, "configuration.secret", request.IdempotencyKey, config.UserMutationScope(), "secret:"+request.Name, request.ExpectedRevision, current, command.CreatedAt, result)
	}
	written, err := s.files.PutSecret(ctx, request.Name, request.Value, request.ExpectedRevision)
	if err != nil {
		_ = s.recordRejected(ctx, "configuration.secret", request.IdempotencyKey, config.UserMutationScope(), "secret:"+request.Name, classify(err), command.CreatedAt, err)
		return SecretReceipt{}, err
	}
	result := SecretReceipt{SchemaVersion: 1, Name: written.Name, Revision: written.Revision, Restart: config.RestartDaemon}
	return result, s.recordAndComplete(ctx, "configuration.secret", request.IdempotencyKey, config.UserMutationScope(), "secret:"+request.Name, request.ExpectedRevision, written.Revision, command.CreatedAt, result)
}

func (s *Service) finishApply(ctx context.Context, request ApplyRequest, descriptor config.SettingDescriptor, target configurationstore.Target, snapshot configurationstore.Snapshot, recovered bool, occurredAt time.Time) (ApplyResult, error) {
	state, err := s.state(ctx, request.Scope, target, &snapshot)
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{SchemaVersion: 1, State: state, Restart: descriptor.Restart, Replayed: recovered}
	return result, s.recordAndComplete(ctx, "configuration.apply", request.IdempotencyKey, request.Scope, request.Key, request.ExpectedRevision, snapshot.Revision, occurredAt, result)
}

func (s *Service) validate(ctx context.Context, request MutationRequest) (configurationstore.Target, config.SettingDescriptor, configurationstore.Mutation, error) {
	target, err := s.authorize(ctx, request.Scope)
	if err != nil {
		return 0, config.SettingDescriptor{}, configurationstore.Mutation{}, err
	}
	descriptor, found := config.LookupSetting(request.Key)
	if !found {
		return 0, config.SettingDescriptor{}, configurationstore.Mutation{}, fmt.Errorf("%w: unsupported setting %q", ErrInvalidRequest, request.Key)
	}
	allowed := false
	for _, scope := range descriptor.AllowedScopes {
		if scope == request.Scope.Kind() {
			allowed = true
		}
	}
	if !allowed {
		return 0, config.SettingDescriptor{}, configurationstore.Mutation{}, fmt.Errorf("%w: setting %s cannot be changed at %s scope", ErrInvalidRequest, request.Key, request.Scope.Kind())
	}
	if err := validateRevision(request.ExpectedRevision); err != nil {
		return 0, config.SettingDescriptor{}, configurationstore.Mutation{}, err
	}
	path := strings.Split(request.Key, ".")
	switch request.Change.Operation() {
	case ChangeUnset:
		return target, descriptor, configurationstore.Mutation{Operation: configurationstore.OperationUnset, Path: path}, nil
	case ChangeSet:
		value, _ := request.Change.Value()
		if err := descriptor.ValidateValue(value); err != nil {
			return 0, config.SettingDescriptor{}, configurationstore.Mutation{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		if descriptor.Constraints.ExistingFile {
			info, err := os.Stat(value.Value().(string))
			if err != nil || info.IsDir() {
				return 0, config.SettingDescriptor{}, configurationstore.Mutation{}, fmt.Errorf("%w: setting %s must name an existing file", ErrInvalidRequest, request.Key)
			}
		}
		return target, descriptor, configurationstore.Mutation{Operation: configurationstore.OperationSet, Path: path, Value: value.Value()}, nil
	default:
		return 0, config.SettingDescriptor{}, configurationstore.Mutation{}, fmt.Errorf("%w: change is required", ErrInvalidRequest)
	}
}

func (s *Service) authorize(ctx context.Context, scope config.MutationScope) (configurationstore.Target, error) {
	switch scope.Kind() {
	case config.MutationScopeUser:
		return configurationstore.TargetUser, nil
	case config.MutationScopeProject:
		project, err := s.audit.Project(ctx, scope.ProjectID())
		if errors.Is(err, statestore.ErrNotFound) {
			return 0, ErrProjectNotFound
		}
		if err != nil {
			return 0, err
		}
		if project.Status != statestore.ProjectActive || project.SourceHash != digest(s.projectRoot) {
			return 0, ErrProjectMismatch
		}
		return configurationstore.TargetProject, nil
	default:
		return 0, fmt.Errorf("%w: mutation scope is required", ErrInvalidRequest)
	}
}

func (s *Service) state(ctx context.Context, scope config.MutationScope, target configurationstore.Target, replacement *configurationstore.Snapshot) (State, error) {
	user, err := s.files.Snapshot(ctx, configurationstore.TargetUser)
	if err != nil {
		return State{}, err
	}
	project, err := s.files.Snapshot(ctx, configurationstore.TargetProject)
	if err != nil {
		return State{}, err
	}
	selected := user
	if target == configurationstore.TargetProject {
		selected = project
	}
	if replacement != nil {
		selected = *replacement
		if target == configurationstore.TargetUser {
			user = *replacement
		} else {
			project = *replacement
		}
	}
	defaultsMap := map[string]any{}
	for _, descriptor := range config.SupportedCatalog().Settings {
		setPath(defaultsMap, strings.Split(descriptor.Key, "."), descriptor.Default.Value())
	}
	defaults, _ := config.Defaults(defaultsMap)
	layers := []config.Layer{}
	if user.Present {
		layer, layerErr := config.UserFile(user.Reference, user.Values)
		if layerErr != nil {
			return State{}, layerErr
		}
		layers = append(layers, layer)
	}
	if project.Present {
		layer, layerErr := config.ProjectFile(project.Reference, project.Values)
		if layerErr != nil {
			return State{}, layerErr
		}
		layers = append(layers, layer)
	}
	effective, err := config.Resolve(defaults, layers...)
	if err != nil {
		return State{}, err
	}
	state := State{SchemaVersion: 1, Scope: scope, Revision: selected.Revision, Configured: configuredSettings(selected.Values), Effective: effectiveSettings(effective)}
	if target == configurationstore.TargetUser {
		state.SecretRevision, err = s.files.SecretRevision(ctx)
	}
	return state, err
}

func configuredSettings(values map[string]any) []ScopedSetting {
	result := []ScopedSetting{}
	for _, descriptor := range config.SupportedCatalog().Settings {
		if raw, ok := lookupPath(values, strings.Split(descriptor.Key, ".")); ok {
			if value, ok := primitiveValue(descriptor.Type, raw); ok {
				result = append(result, ScopedSetting{Key: descriptor.Key, Value: value})
			}
		}
	}
	return result
}
func effectiveSettings(effective config.Effective) []EffectiveSetting {
	result := []EffectiveSetting{}
	for _, descriptor := range config.SupportedCatalog().Settings {
		segments := strings.Split(descriptor.Key, ".")
		resolved, ok := effective.Lookup(segments...)
		if !ok {
			continue
		}
		if value, ok := primitiveValue(descriptor.Type, resolved.Value()); ok {
			result = append(result, EffectiveSetting{Key: descriptor.Key, Value: value, Source: resolved.Source()})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
func primitiveValue(kind config.SettingType, raw any) (config.TypedValue, bool) {
	switch kind {
	case config.SettingString:
		v, ok := raw.(string)
		return config.StringValue(v), ok
	case config.SettingEnum:
		v, ok := raw.(string)
		return config.EnumValue(v), ok
	case config.SettingPath:
		v, ok := raw.(string)
		return config.PathValue(v), ok
	case config.SettingSecretReference:
		v, ok := raw.(string)
		return config.SecretReferenceValue(v), ok
	case config.SettingInteger:
		switch v := raw.(type) {
		case int:
			return config.IntegerValue(int64(v)), true
		case int64:
			return config.IntegerValue(v), true
		}
	case config.SettingBoolean:
		v, ok := raw.(bool)
		return config.BooleanValue(v), ok
	}
	return config.TypedValue{}, false
}
func setPath(values map[string]any, path []string, value any) {
	current := values
	for _, segment := range path[:len(path)-1] {
		child, ok := current[segment].(map[string]any)
		if !ok {
			child = map[string]any{}
			current[segment] = child
		}
		current = child
	}
	current[path[len(path)-1]] = value
}
func lookupPath(values map[string]any, path []string) (any, bool) {
	var current any = values
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func (s *Service) begin(ctx context.Context, scope, key string, request any) (statestore.CommandEvidence, bool, error) {
	if strings.TrimSpace(key) != key || len(key) < 8 || len(key) > 128 {
		return statestore.CommandEvidence{}, false, fmt.Errorf("%w: idempotency key must be 8-128 bytes without surrounding whitespace", ErrInvalidRequest)
	}
	encoded, _ := json.Marshal(request)
	return s.audit.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: scope, IdempotencyKey: key, RequestDigest: digest(string(encoded)), CreatedAt: s.now().UTC().Round(0)})
}
func (s *Service) recordAndComplete(ctx context.Context, commandScope, key string, scope config.MutationScope, setting, before, after string, occurredAt time.Time, result any) error {
	data, _ := json.Marshal(map[string]any{"scope": scope.Kind(), "projectId": scope.ProjectID(), "setting": setting, "previousRevision": before, "revision": after, "outcome": "accepted"})
	operationID := identity.Deterministic("operation_", commandScope+"\x00"+key)
	events, err := s.audit.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Deterministic("event_", commandScope+"\x00"+key), AggregateType: statestore.AggregateOperation, AggregateID: operationID, ExpectedRevision: 0, Kind: "configuration.change_recorded", OccurredAt: occurredAt.UTC().Round(0), CorrelationID: operationID, CommandID: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, Data: data, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(result)
	first, last := events[0].GlobalPosition, events[len(events)-1].GlobalPosition
	_, err = s.audit.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: commandScope, IdempotencyKey: key, ResponseStatus: 200, Response: encoded, FirstEventPosition: &first, LastEventPosition: &last, CompletedAt: s.now().UTC().Round(0)})
	return err
}
func (s *Service) rejected(ctx context.Context, key string, scope config.MutationScope, setting, reason string) {
	data, _ := json.Marshal(map[string]any{"scope": scope.Kind(), "projectId": scope.ProjectID(), "setting": setting, "outcome": "rejected", "reason": reason})
	operationID := identity.Random("operation_")
	_, _ = s.audit.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateOperation, AggregateID: operationID, ExpectedRevision: 0, Kind: "configuration.change_rejected", OccurredAt: s.now().UTC().Round(0), CorrelationID: operationID, CommandID: "rejected-" + digest(key)[:24], Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, Data: data, Metadata: json.RawMessage(`{}`)})
}

type commandFailure struct {
	Code string `json:"code"`
}

func (s *Service) recordRejected(ctx context.Context, commandScope, key string, scope config.MutationScope, setting, reason string, occurredAt time.Time, original error) error {
	data, _ := json.Marshal(map[string]any{"scope": scope.Kind(), "projectId": scope.ProjectID(), "setting": setting, "outcome": "rejected", "reason": reason})
	operationID := identity.Deterministic("operation_", commandScope+"\x00"+key)
	events, err := s.audit.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Deterministic("event_", commandScope+"\x00"+key), AggregateType: statestore.AggregateOperation, AggregateID: operationID, ExpectedRevision: 0, Kind: "configuration.change_rejected", OccurredAt: occurredAt.UTC().Round(0), CorrelationID: operationID, CommandID: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, Data: data, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(commandFailure{Code: reason})
	first, last := events[0].GlobalPosition, events[len(events)-1].GlobalPosition
	status := 503
	if errors.Is(original, configurationstore.ErrRevisionConflict) {
		status = 409
	} else if errors.Is(original, configurationstore.ErrNoPrevious) {
		status = 404
	} else if errors.Is(original, configurationstore.ErrPathBoundary) {
		status = 422
	}
	_, err = s.audit.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: commandScope, IdempotencyKey: key, ResponseStatus: status, Response: encoded, FirstEventPosition: &first, LastEventPosition: &last, CompletedAt: s.now().UTC().Round(0)})
	return err
}

func replayFailure(encoded json.RawMessage) error {
	var failure commandFailure
	if json.Unmarshal(encoded, &failure) != nil {
		return ErrCommandInProgress
	}
	switch failure.Code {
	case "revision_conflict":
		return configurationstore.ErrRevisionConflict
	case "path_boundary":
		return configurationstore.ErrPathBoundary
	case "write_failed":
		return errors.New("configuration write failed")
	default:
		return configurationstore.ErrNoPrevious
	}
}
func classify(err error) string {
	switch {
	case errors.Is(err, configurationstore.ErrRevisionConflict):
		return "revision_conflict"
	case errors.Is(err, configurationstore.ErrPathBoundary):
		return "path_boundary"
	default:
		return "write_failed"
	}
}
func validateRevision(value string) error {
	if len(value) != 64 {
		return fmt.Errorf("%w: expectedRevision must be a SHA-256 digest", ErrInvalidRequest)
	}
	_, err := hex.DecodeString(value)
	if err != nil {
		return fmt.Errorf("%w: expectedRevision must be a SHA-256 digest", ErrInvalidRequest)
	}
	return nil
}
func digest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

var secretNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
