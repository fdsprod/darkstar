// Package command implements deterministic argument-array command execution.
package command

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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"darkstar/src/core/attemptexecution"
	"darkstar/src/ports"
	executorport "darkstar/src/ports/executor"
)

const (
	Kind                  = "command"
	defaultOutputLimit    = 4 << 20
	commandEvidenceKind   = "command-execution"
	commandEvidenceSchema = 1
)

// Definition contains only data supplied by a workflow. Argv[0] names an
// executable alias from Policy; CWD is a workspace-relative allowlist entry;
// Environment selects values owned by Policy instead of supplying new values.
type Definition struct {
	Argv        []string
	CWD         string
	Environment []string
}

// Policy is the sole authority for executable paths, working directories, and
// environment values available to a command.
type Policy struct {
	Executables        map[string]string
	WorkingDirectories []string
	Environment        map[string]string
	OutputLimitBytes   int
}

// Classification is the stable terminal classification recorded for every
// process that starts.
type Classification string

const (
	ClassificationSucceeded     Classification = "succeeded"
	ClassificationFailed        Classification = "failed"
	ClassificationTimedOut      Classification = "timed_out"
	ClassificationCancelled     Classification = "cancelled"
	ClassificationOutputLimited Classification = "output_limited"
	ClassificationUncertain     Classification = "uncertain"
)

// Completion is a closed terminal outcome. Only Failed carries an exit code,
// so timeout and cancellation cannot accidentally be reported as normal exits.
type Completion interface {
	Classification() Classification
	isCompletion()
}

type Succeeded struct{}

func (Succeeded) Classification() Classification { return ClassificationSucceeded }
func (Succeeded) isCompletion()                  {}

type Failed struct{ ExitCode int }

func (Failed) Classification() Classification { return ClassificationFailed }
func (Failed) isCompletion()                  {}

type TimedOut struct{}

func (TimedOut) Classification() Classification { return ClassificationTimedOut }
func (TimedOut) isCompletion()                  {}

type Cancelled struct{}

func (Cancelled) Classification() Classification { return ClassificationCancelled }
func (Cancelled) isCompletion()                  {}

type OutputLimited struct{}

func (OutputLimited) Classification() Classification { return ClassificationOutputLimited }
func (OutputLimited) isCompletion()                  {}

type Uncertain struct{}

func (Uncertain) Classification() Classification { return ClassificationUncertain }
func (Uncertain) isCompletion()                  {}

// EvidenceRecord is one immutable command observation to persist.
type EvidenceRecord struct {
	AttemptID string
	Kind      string
	MediaType string
	Data      []byte
}

// EvidenceRecorder stores captured command evidence behind a controlled ref.
type EvidenceRecorder interface {
	Record(context.Context, EvidenceRecord) (executorport.Evidence, error)
}

type environmentValue struct {
	name  string
	value string
}

type resolvedPolicy struct {
	executables map[string]string
	directories map[string]string
	environment map[string]environmentValue
	outputLimit int
}

// Adapter runs one configured workflow command through the generic executor
// lifecycle used by the attempt runner.
type Adapter struct {
	definition Definition
	policy     resolvedPolicy
	recorder   EvidenceRecorder
}

var _ executorport.Executor = (*Adapter)(nil)

// New constructs an adapter after resolving every executable to an exact path.
func New(definition Definition, policy Policy, recorder EvidenceRecorder) (*Adapter, error) {
	if recorder == nil {
		return nil, errors.New("command evidence recorder is required")
	}
	resolved, err := resolvePolicy(policy)
	if err != nil {
		return nil, err
	}
	definition, err = resolved.validateDefinition(definition)
	if err != nil {
		return nil, err
	}
	return &Adapter{definition: definition, policy: resolved, recorder: recorder}, nil
}

func (*Adapter) Kind() string { return Kind }

func (adapter *Adapter) Start(ctx context.Context, request executorport.Request) (executorport.Execution, error) {
	if adapter == nil || adapter.recorder == nil {
		return nil, failure(ports.FailureInternal, "command executor is not configured", false, nil)
	}
	if strings.TrimSpace(request.AttemptID) == "" || request.AttemptID != strings.TrimSpace(request.AttemptID) {
		return nil, failure(ports.FailureInvalidRequest, "command attempt ID is required and must be trimmed", false, nil)
	}
	if request.Timeout <= 0 {
		return nil, failure(ports.FailureInvalidRequest, "command timeout must be positive", false, nil)
	}
	return start(ctx, request.AttemptID, request.Workspace, request.Timeout, adapter.definition, adapter.policy, adapter.recorder, nil)
}

func (*Adapter) Resume(context.Context, executorport.ResumeRequest) (executorport.Execution, error) {
	return nil, failure(ports.FailureUnsupported, "command processes cannot be reattached after ownership is lost", false, nil)
}

// Validator executes ordered deterministic commands with the candidate JSON on
// stdin. Its successful evidence is returned to the attempt's atomic commit.
type Validator struct {
	definitions []Definition
	policy      resolvedPolicy
	recorder    EvidenceRecorder
	timeout     time.Duration
}

var _ attemptexecution.OutputValidator = (*Validator)(nil)

func NewValidator(definitions []Definition, policy Policy, recorder EvidenceRecorder, timeout time.Duration) (*Validator, error) {
	if len(definitions) == 0 {
		return nil, errors.New("at least one validation command is required")
	}
	if recorder == nil {
		return nil, errors.New("command evidence recorder is required")
	}
	if timeout <= 0 {
		return nil, errors.New("validation command timeout must be positive")
	}
	resolved, err := resolvePolicy(policy)
	if err != nil {
		return nil, err
	}
	values := make([]Definition, len(definitions))
	for index, definition := range definitions {
		values[index], err = resolved.validateDefinition(definition)
		if err != nil {
			return nil, fmt.Errorf("validation command %d: %w", index+1, err)
		}
	}
	return &Validator{definitions: values, policy: resolved, recorder: recorder, timeout: timeout}, nil
}

func (validator *Validator) Validate(ctx context.Context, request attemptexecution.ValidationRequest) (attemptexecution.ValidationResult, error) {
	if validator == nil || validator.recorder == nil {
		return attemptexecution.ValidationResult{}, failure(ports.FailureInternal, "command validator is not configured", false, nil)
	}
	result := attemptexecution.ValidationResult{}
	for index, definition := range validator.definitions {
		attemptID := fmt.Sprintf("%s-validation-%03d", request.AttemptID, index+1)
		execution, err := start(ctx, attemptID, request.Workspace, validator.timeout, definition, validator.policy, validator.recorder, request.Result.CandidateOutput)
		if err != nil {
			return result, fmt.Errorf("validation command %d: %w", index+1, err)
		}
		outcome, err := execution.(*processExecution).waitOutcome(ctx)
		result.Evidence = append(result.Evidence, outcome.evidence)
		if err != nil {
			return result, fmt.Errorf("validation command %d: %w", index+1, err)
		}
	}
	return result, nil
}

type processExecution struct {
	reference executorport.Reference
	events    *eventStream
	done      chan struct{}
	cancel    chan cancelRequest

	mu             sync.Mutex
	outcome        processOutcome
	evidenceError  error
	cancelledByKey map[string]executorport.CancelResult
	cancelMu       sync.Mutex
}

type processOutcome struct {
	completion Completion
	stdout     []byte
	stderr     []byte
	evidence   executorport.Evidence
}

type cancelRequest struct {
	key      string
	grace    time.Duration
	response chan executorport.CancelResult
}

func (execution *processExecution) Reference() executorport.Reference { return execution.reference }
func (execution *processExecution) Events() executorport.Events       { return execution.events }

func (execution *processExecution) Wait(ctx context.Context) (executorport.Result, error) {
	outcome, err := execution.waitOutcome(ctx)
	if err != nil {
		return executorport.Result{}, err
	}
	return executorport.Result{
		CandidateOutput: append(json.RawMessage(nil), outcome.stdout...),
		Evidence:        []executorport.Evidence{outcome.evidence},
		RecoveryRef:     execution.reference.RecoveryRef,
	}, nil
}

func (execution *processExecution) waitOutcome(ctx context.Context) (processOutcome, error) {
	select {
	case <-ctx.Done():
		return processOutcome{}, ctx.Err()
	case <-execution.done:
	}
	execution.mu.Lock()
	outcome := execution.outcome
	evidenceErr := execution.evidenceError
	execution.mu.Unlock()
	if evidenceErr != nil {
		return outcome, failure(ports.FailureInternal, "command evidence could not be persisted", false, nil)
	}
	if _, ok := outcome.completion.(Succeeded); !ok {
		return outcome, &ExecutionError{Completion: outcome.completion, Evidence: outcome.evidence}
	}
	return outcome, nil
}

func (execution *processExecution) Cancel(ctx context.Context, request executorport.CancelRequest) (executorport.CancelResult, error) {
	if strings.TrimSpace(request.IdempotencyKey) == "" || request.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) || request.GracePeriod < 0 {
		return executorport.CancelResult{}, failure(ports.FailureInvalidRequest, "cancel key must be trimmed and grace period cannot be negative", false, nil)
	}
	execution.cancelMu.Lock()
	defer execution.cancelMu.Unlock()
	execution.mu.Lock()
	if result, exists := execution.cancelledByKey[request.IdempotencyKey]; exists {
		execution.mu.Unlock()
		return result, nil
	}
	execution.mu.Unlock()
	response := make(chan executorport.CancelResult, 1)
	select {
	case <-execution.done:
		result := executorport.CancelResult{Disposition: executorport.CancelAlreadyTerminal}
		execution.rememberCancel(request.IdempotencyKey, result)
		return result, nil
	case execution.cancel <- cancelRequest{key: request.IdempotencyKey, grace: request.GracePeriod, response: response}:
	case <-ctx.Done():
		return executorport.CancelResult{}, ctx.Err()
	}
	select {
	case result := <-response:
		execution.rememberCancel(request.IdempotencyKey, result)
		return result, nil
	case <-execution.done:
		result := executorport.CancelResult{Disposition: executorport.CancelAlreadyTerminal}
		execution.rememberCancel(request.IdempotencyKey, result)
		return result, nil
	case <-ctx.Done():
		return executorport.CancelResult{}, ctx.Err()
	}
}

func (execution *processExecution) rememberCancel(key string, result executorport.CancelResult) {
	execution.mu.Lock()
	execution.cancelledByKey[key] = result
	execution.mu.Unlock()
}

// ExecutionError reports a stable terminal class and its evidence reference.
type ExecutionError struct {
	Completion Completion
	Evidence   executorport.Evidence
}

func (err *ExecutionError) Error() string {
	if err == nil || err.Completion == nil {
		return "command execution failed"
	}
	if failed, ok := err.Completion.(Failed); ok {
		return fmt.Sprintf("command exited with code %d", failed.ExitCode)
	}
	return "command execution " + string(err.Completion.Classification())
}

func (err *ExecutionError) Unwrap() error {
	if err == nil || err.Completion == nil {
		return failure(ports.FailureInternal, "command execution failed without a classification", false, nil)
	}
	details := map[string]string{"classification": string(err.Completion.Classification())}
	if failed, ok := err.Completion.(Failed); ok {
		details["exitCode"] = fmt.Sprint(failed.ExitCode)
	}
	if err.Evidence.Ref != "" {
		details["evidenceRef"] = err.Evidence.Ref
	}
	switch err.Completion.(type) {
	case Failed:
		return failure(ports.FailureInvalidRequest, "command returned a nonzero exit status", false, details)
	case TimedOut:
		return failure(ports.FailureTimeout, "command exceeded its timeout", true, details)
	case Cancelled:
		return failure(ports.FailureCancelled, "command was cancelled", false, details)
	case OutputLimited:
		return failure(ports.FailureResourceExhausted, "command output exceeded its configured limit", false, details)
	default:
		return failure(ports.FailureUncertain, "command termination could not be classified", false, details)
	}
}

type eventStream struct{ channel <-chan executorport.Event }

func (stream *eventStream) Receive() (executorport.Event, error) {
	event, ok := <-stream.channel
	if !ok {
		return executorport.Event{}, io.EOF
	}
	return event, nil
}

func (*eventStream) Close() error { return nil }

func start(ctx context.Context, attemptID, workspace string, timeout time.Duration, definition Definition, policy resolvedPolicy, recorder EvidenceRecorder, stdin []byte) (executorport.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prepared, err := prepare(workspace, definition, policy)
	if err != nil {
		return nil, err
	}
	stdout := newBoundedCapture(policy.outputLimit)
	stderr := newBoundedCapture(policy.outputLimit)
	command := exec.Command(prepared.executable, prepared.arguments...)
	command.Dir = prepared.directory
	command.Env = prepared.environment
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	configureOwnedProcess(command)
	if err := command.Start(); err != nil {
		return nil, failure(ports.FailureUnavailable, "command process could not be started", true, nil)
	}
	owner, err := newProcessOwner(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, failure(ports.FailureUncertain, "command process ownership could not be established", false, nil)
	}

	eventChannel := make(chan executorport.Event, 2)
	execution := &processExecution{
		reference: executorport.Reference{AttemptID: attemptID, ExternalID: fmt.Sprint(owner.PID()), RecoveryRef: fmt.Sprintf("command:%s:%d", attemptID, owner.PID())},
		events:    &eventStream{channel: eventChannel}, done: make(chan struct{}), cancel: make(chan cancelRequest, 1),
		cancelledByKey: map[string]executorport.CancelResult{},
	}
	startedAt := time.Now().UTC()
	eventChannel <- commandEvent("1", "command.started", startedAt, map[string]any{
		"executable": prepared.executable, "workingDirectory": prepared.directory, "argumentDigest": prepared.argumentDigest,
	}, "")
	go execution.observe(ctx, timeout, owner, prepared, stdout, stderr, recorder, eventChannel, startedAt)
	return execution, nil
}

type preparedCommand struct {
	executable      string
	arguments       []string
	directory       string
	environment     []string
	environmentKeys []string
	argumentDigest  string
}

func prepare(workspace string, definition Definition, policy resolvedPolicy) (preparedCommand, error) {
	if strings.TrimSpace(workspace) == "" || workspace != strings.TrimSpace(workspace) || !filepath.IsAbs(workspace) {
		return preparedCommand{}, failure(ports.FailureInvalidRequest, "command workspace must be an absolute path", false, nil)
	}
	root, err := canonicalDirectory(workspace)
	if err != nil {
		return preparedCommand{}, failure(ports.FailureInvalidRequest, "command workspace is unavailable", false, nil)
	}
	relative := definition.CWD
	if relative == "" {
		relative = "."
	}
	directory, err := canonicalDirectory(filepath.Join(root, relative))
	if err != nil || !pathWithin(root, directory) {
		return preparedCommand{}, failure(ports.FailurePermissionDenied, "command working directory is outside the allowed workspace", false, nil)
	}
	allowedRelative, err := filepath.Rel(root, directory)
	if err != nil {
		return preparedCommand{}, failure(ports.FailureInvalidRequest, "command working directory cannot be resolved", false, nil)
	}
	if _, allowed := policy.directories[normalizePath(allowedRelative)]; !allowed {
		return preparedCommand{}, failure(ports.FailurePermissionDenied, "command working directory is not allowlisted", false, nil)
	}

	environment := make([]string, 0, len(definition.Environment))
	environmentKeys := make([]string, 0, len(definition.Environment))
	for _, requested := range definition.Environment {
		value := policy.environment[normalizeEnvironmentName(requested)]
		environment = append(environment, value.name+"="+value.value)
		environmentKeys = append(environmentKeys, value.name)
	}
	sort.Strings(environment)
	sort.Strings(environmentKeys)
	encodedArguments, _ := json.Marshal(definition.Argv)
	digest := sha256.Sum256(encodedArguments)
	return preparedCommand{
		executable: policy.executables[definition.Argv[0]], arguments: append([]string(nil), definition.Argv[1:]...),
		directory: directory, environment: environment, environmentKeys: environmentKeys,
		argumentDigest: hex.EncodeToString(digest[:]),
	}, nil
}

func (execution *processExecution) observe(ctx context.Context, timeout time.Duration, owner *processOwner, prepared preparedCommand, stdout, stderr *boundedCapture, recorder EvidenceRecorder, events chan<- executorport.Event, startedAt time.Time) {
	wait := make(chan error, 1)
	go func() { wait <- owner.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var completion Completion
	var waitErr error
	var cancelResponse chan executorport.CancelResult
	var cancelDisposition executorport.CancelDisposition
	select {
	case waitErr = <-wait:
		completion = naturalCompletion(waitErr)
	case <-timer.C:
		completion = TimedOut{}
		if err := owner.Kill(); err != nil {
			completion = Uncertain{}
		}
		waitErr = <-wait
	case <-ctx.Done():
		completion = Cancelled{}
		if err := owner.Kill(); err != nil {
			completion = Uncertain{}
		}
		waitErr = <-wait
	case <-stdout.overflow:
		completion = OutputLimited{}
		if err := owner.Kill(); err != nil {
			completion = Uncertain{}
		}
		waitErr = <-wait
	case <-stderr.overflow:
		completion = OutputLimited{}
		if err := owner.Kill(); err != nil {
			completion = Uncertain{}
		}
		waitErr = <-wait
	case request := <-execution.cancel:
		cancelResponse = request.response
		completion = Cancelled{}
		graceful, terminateErr := owner.Terminate()
		if terminateErr != nil || !graceful || request.grace == 0 {
			if err := owner.Kill(); err != nil {
				completion = Uncertain{}
				cancelDisposition = executorport.CancelUncertain
			} else {
				cancelDisposition = executorport.CancelForced
			}
			waitErr = <-wait
		} else {
			graceTimer := time.NewTimer(request.grace)
			select {
			case waitErr = <-wait:
				cancelDisposition = executorport.CancelGraceful
			case <-graceTimer.C:
				if err := owner.Kill(); err != nil {
					completion = Uncertain{}
					cancelDisposition = executorport.CancelUncertain
				} else {
					cancelDisposition = executorport.CancelForced
				}
				waitErr = <-wait
			}
			if !graceTimer.Stop() {
				select {
				case <-graceTimer.C:
				default:
				}
			}
		}
	}
	_ = waitErr
	if stdout.Truncated() || stderr.Truncated() {
		switch completion.(type) {
		case Succeeded, Failed:
			completion = OutputLimited{}
		}
	}

	finishedAt := time.Now().UTC()
	outcome := processOutcome{completion: completion, stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	evidence, evidenceErr := recordEvidence(recorder, execution.reference.AttemptID, prepared, outcome, stdout.Truncated(), stderr.Truncated(), startedAt, finishedAt)
	outcome.evidence = evidence
	execution.mu.Lock()
	execution.outcome = outcome
	execution.evidenceError = evidenceErr
	execution.mu.Unlock()
	events <- commandEvent("2", "command.completed", finishedAt, completionData(completion), evidence.Ref)
	close(events)
	if cancelResponse != nil {
		cancelResponse <- executorport.CancelResult{Disposition: cancelDisposition, EvidenceRef: evidence.Ref}
	}
	close(execution.done)
}

func naturalCompletion(err error) Completion {
	if err == nil {
		return Succeeded{}
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return Failed{ExitCode: exitError.ExitCode()}
	}
	return Uncertain{}
}

func completionData(completion Completion) map[string]any {
	data := map[string]any{"classification": completion.Classification()}
	if failed, ok := completion.(Failed); ok {
		data["exitCode"] = failed.ExitCode
	}
	return data
}

func commandEvent(cursor, kind string, occurredAt time.Time, data map[string]any, evidenceRef string) executorport.Event {
	encoded, _ := json.Marshal(data)
	return executorport.Event{Cursor: cursor, Kind: kind, OccurredAt: occurredAt, Data: encoded, EvidenceRef: evidenceRef}
}

func recordEvidence(recorder EvidenceRecorder, attemptID string, prepared preparedCommand, outcome processOutcome, stdoutTruncated, stderrTruncated bool, startedAt, finishedAt time.Time) (executorport.Evidence, error) {
	document := map[string]any{
		"schemaVersion":    commandEvidenceSchema,
		"attemptId":        attemptID,
		"executable":       prepared.executable,
		"workingDirectory": prepared.directory,
		"argumentDigest":   prepared.argumentDigest,
		"argumentCount":    len(prepared.arguments) + 1,
		"environment":      prepared.environmentKeys,
		"classification":   outcome.completion.Classification(),
		"stdout":           string(outcome.stdout),
		"stderr":           string(outcome.stderr),
		"stdoutTruncated":  stdoutTruncated,
		"stderrTruncated":  stderrTruncated,
		"startedAt":        startedAt.Format(time.RFC3339Nano),
		"finishedAt":       finishedAt.Format(time.RFC3339Nano),
	}
	if failed, ok := outcome.completion.(Failed); ok {
		document["exitCode"] = failed.ExitCode
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return executorport.Evidence{}, err
	}
	return recorder.Record(context.WithoutCancel(context.Background()), EvidenceRecord{
		AttemptID: attemptID, Kind: commandEvidenceKind, MediaType: "application/json", Data: encoded,
	})
}

type boundedCapture struct {
	mu        sync.Mutex
	limit     int
	buffer    bytes.Buffer
	truncated bool
	overflow  chan struct{}
	once      sync.Once
}

func newBoundedCapture(limit int) *boundedCapture {
	return &boundedCapture{limit: limit, overflow: make(chan struct{})}
}

func (capture *boundedCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	remaining := capture.limit - capture.buffer.Len()
	if remaining > 0 {
		write := len(data)
		if write > remaining {
			write = remaining
		}
		_, _ = capture.buffer.Write(data[:write])
	}
	if len(data) > remaining {
		capture.truncated = true
		capture.once.Do(func() { close(capture.overflow) })
	}
	return len(data), nil
}

func (capture *boundedCapture) Bytes() []byte {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]byte(nil), capture.buffer.Bytes()...)
}

func (capture *boundedCapture) Truncated() bool {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.truncated
}

func resolvePolicy(policy Policy) (resolvedPolicy, error) {
	if len(policy.Executables) == 0 {
		return resolvedPolicy{}, errors.New("command policy requires at least one executable")
	}
	if len(policy.WorkingDirectories) == 0 {
		return resolvedPolicy{}, errors.New("command policy requires at least one working directory")
	}
	resolved := resolvedPolicy{
		executables: map[string]string{}, directories: map[string]string{}, environment: map[string]environmentValue{},
		outputLimit: policy.OutputLimitBytes,
	}
	if resolved.outputLimit == 0 {
		resolved.outputLimit = defaultOutputLimit
	}
	if resolved.outputLimit < 0 {
		return resolvedPolicy{}, errors.New("command output limit cannot be negative")
	}
	for alias, path := range policy.Executables {
		if strings.TrimSpace(alias) == "" || alias != strings.TrimSpace(alias) || strings.ContainsRune(alias, os.PathSeparator) {
			return resolvedPolicy{}, errors.New("command executable aliases must be non-path names")
		}
		if !filepath.IsAbs(path) {
			return resolvedPolicy{}, fmt.Errorf("command executable %q must use an absolute path", alias)
		}
		canonical, err := canonicalFile(path)
		if err != nil {
			return resolvedPolicy{}, fmt.Errorf("resolve command executable %q: %w", alias, err)
		}
		resolved.executables[alias] = canonical
	}
	for _, directory := range policy.WorkingDirectories {
		normalized, err := normalizeRelativeDirectory(directory)
		if err != nil {
			return resolvedPolicy{}, err
		}
		key := normalizePath(normalized)
		if _, duplicate := resolved.directories[key]; duplicate {
			return resolvedPolicy{}, fmt.Errorf("duplicate command working directory %q", directory)
		}
		resolved.directories[key] = normalized
	}
	for name, value := range policy.Environment {
		if !validEnvironmentName(name) || strings.ContainsRune(value, '\x00') {
			return resolvedPolicy{}, fmt.Errorf("command environment entry %q is invalid", name)
		}
		key := normalizeEnvironmentName(name)
		if _, duplicate := resolved.environment[key]; duplicate {
			return resolvedPolicy{}, fmt.Errorf("duplicate command environment entry %q", name)
		}
		resolved.environment[key] = environmentValue{name: name, value: value}
	}
	return resolved, nil
}

func (policy resolvedPolicy) validateDefinition(definition Definition) (Definition, error) {
	if len(definition.Argv) == 0 || strings.TrimSpace(definition.Argv[0]) == "" {
		return Definition{}, errors.New("command argv requires an executable alias")
	}
	if _, allowed := policy.executables[definition.Argv[0]]; !allowed {
		return Definition{}, fmt.Errorf("command executable alias %q is not allowlisted", definition.Argv[0])
	}
	for _, argument := range definition.Argv {
		if strings.ContainsRune(argument, '\x00') {
			return Definition{}, errors.New("command arguments cannot contain NUL")
		}
	}
	directory, err := normalizeRelativeDirectory(definition.CWD)
	if err != nil {
		return Definition{}, err
	}
	if _, allowed := policy.directories[normalizePath(directory)]; !allowed {
		return Definition{}, fmt.Errorf("command working directory %q is not allowlisted", directory)
	}
	seen := map[string]struct{}{}
	environment := make([]string, len(definition.Environment))
	for index, name := range definition.Environment {
		key := normalizeEnvironmentName(name)
		if _, allowed := policy.environment[key]; !allowed {
			return Definition{}, fmt.Errorf("command environment entry %q is not allowlisted", name)
		}
		if _, duplicate := seen[key]; duplicate {
			return Definition{}, fmt.Errorf("duplicate command environment entry %q", name)
		}
		seen[key] = struct{}{}
		environment[index] = policy.environment[key].name
	}
	return Definition{Argv: append([]string(nil), definition.Argv...), CWD: directory, Environment: environment}, nil
}

func normalizeRelativeDirectory(value string) (string, error) {
	if value == "" {
		value = "."
	}
	if value != strings.TrimSpace(value) || filepath.IsAbs(value) {
		return "", errors.New("command working directories must be trimmed relative paths")
	}
	clean := filepath.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("command working directories cannot escape the workspace")
	}
	return clean, nil
}

func canonicalFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("path is a directory")
	}
	value, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Some supported Windows filesystems deny the handle operation used by
		// EvalSymlinks for ordinary executable files. The absolute clean spelling
		// remains pinned; DS-190 owns complete reparse-point denial.
		return filepath.Clean(path), nil
	}
	return filepath.Clean(value), nil
}

func canonicalDirectory(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	value, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Match executable handling for Windows filesystems that deny the handle
		// query used by EvalSymlinks. Workspace escape hardening is completed by
		// DS-190's canonical path boundary.
		return filepath.Clean(path), nil
	}
	return filepath.Clean(value), nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func normalizePath(value string) string {
	value = filepath.Clean(value)
	if runtime.GOOS == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func validEnvironmentName(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "=\x00")
}

func normalizeEnvironmentName(value string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(value)
	}
	return value
}

func failure(code ports.FailureCode, message string, retryable bool, details map[string]string) error {
	return &ports.Failure{Code: code, Message: message, Retryable: retryable, Details: details}
}
