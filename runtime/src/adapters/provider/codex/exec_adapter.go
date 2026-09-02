package codex

import (
	"bufio"
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

	"darkstar/src/ports"
	providerport "darkstar/src/ports/provider"
)

const (
	execTransport       = "exec-jsonl"
	execSyntheticTurnID = "exec-jsonl-turn"
)

var defaultSupportedExecVersions = []string{"0.151.0-alpha.7.2"}

// ExecProcess is one started, owned codex exec process. Implementations must
// terminate the complete owned process tree from Kill.
type ExecProcess interface {
	Stdout() io.Reader
	Stderr() io.Reader
	PID() int
	Wait() error
	Kill() error
}

// ExecCommand is the exact command selected for a bounded fallback attempt.
// Arguments are separate values so Windows quoting remains the responsibility
// of os/exec rather than a shell.
type ExecCommand struct {
	Executable string
	Arguments  []string
	Directory  string
}

// ExecProcessFactory starts an owned process for a fully prepared command.
type ExecProcessFactory func(ExecCommand) (ExecProcess, error)

// ExecRecoveryRecord is the minimum frozen request material needed to resume a
// killed exec session in a fresh adapter process.
type ExecRecoveryRecord struct {
	AttemptID    string               `json:"attemptId"`
	Workspace    string               `json:"workspace"`
	Prompt       string               `json:"prompt"`
	Inputs       []providerport.Input `json:"inputs,omitempty"`
	OutputSchema json.RawMessage      `json:"outputSchema"`
}

// ExecRecoveryStore persists and reloads bounded exec request material. A
// durable implementation is required for resume after daemon restart.
type ExecRecoveryStore interface {
	SaveExecRecovery(context.Context, ExecRecoveryRecord) error
	LoadExecRecovery(context.Context, string) (ExecRecoveryRecord, error)
}

// DirectoryExecRecoveryStore keeps protected, atomically created immutable
// recovery records beneath a DARKSTAR-owned state directory.
type DirectoryExecRecoveryStore struct{ root string }

// NewDirectoryExecRecoveryStore creates a durable exec recovery store.
func NewDirectoryExecRecoveryStore(root string) (*DirectoryExecRecoveryStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("codex exec recovery root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex exec recovery root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create Codex exec recovery root: %w", err)
	}
	return &DirectoryExecRecoveryStore{root: filepath.Clean(absolute)}, nil
}

func (store *DirectoryExecRecoveryStore) SaveExecRecovery(ctx context.Context, record ExecRecoveryRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(record.AttemptID) == "" || strings.TrimSpace(record.Workspace) == "" || len(bytes.TrimSpace(record.OutputSchema)) == 0 {
		return errors.New("codex exec recovery record requires attempt, workspace, and output schema")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode Codex exec recovery record: %w", err)
	}
	target := filepath.Join(store.root, execRecoveryFileName(record.AttemptID))
	if existing, readErr := os.ReadFile(target); readErr == nil {
		if bytes.Equal(existing, payload) {
			return nil
		}
		return errors.New("codex exec recovery record is immutable")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("inspect Codex exec recovery file: %w", readErr)
	}
	temporary, err := os.CreateTemp(store.root, ".exec-recovery-*.tmp")
	if err != nil {
		return fmt.Errorf("create Codex exec recovery temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect Codex exec recovery file: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		return fmt.Errorf("write Codex exec recovery file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Codex exec recovery file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Codex exec recovery file: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("commit Codex exec recovery file: %w", err)
	}
	committed = true
	return nil
}

func (store *DirectoryExecRecoveryStore) LoadExecRecovery(ctx context.Context, attemptID string) (ExecRecoveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return ExecRecoveryRecord{}, err
	}
	if strings.TrimSpace(attemptID) == "" {
		return ExecRecoveryRecord{}, errors.New("codex exec recovery attempt ID is required")
	}
	payload, err := os.ReadFile(filepath.Join(store.root, execRecoveryFileName(attemptID)))
	if err != nil {
		return ExecRecoveryRecord{}, fmt.Errorf("read Codex exec recovery file: %w", err)
	}
	var record ExecRecoveryRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return ExecRecoveryRecord{}, fmt.Errorf("decode Codex exec recovery file: %w", err)
	}
	if record.AttemptID != attemptID || strings.TrimSpace(record.Workspace) == "" || len(bytes.TrimSpace(record.OutputSchema)) == 0 {
		return ExecRecoveryRecord{}, errors.New("codex exec recovery record identity is invalid")
	}
	record.Inputs = append([]providerport.Input(nil), record.Inputs...)
	record.OutputSchema = cloneRaw(record.OutputSchema)
	return record, nil
}

func execRecoveryFileName(attemptID string) string {
	digest := sha256.Sum256([]byte(attemptID))
	return safePathPart(attemptID) + "-" + hex.EncodeToString(digest[:8]) + ".json"
}

// ExecAdapterOptions configures the exact-version-gated exec JSONL fallback.
type ExecAdapterOptions struct {
	Executable        string
	ProviderVersion   string
	SupportedVersions []string
	Factory           ExecProcessFactory
	EvidenceRecorder  EvidenceRecorder
	RecoveryStore     ExecRecoveryStore
	Clock             func() time.Time
}

// ExecAdapter implements the provider port for bounded, non-interactive,
// read-only nodes using codex exec --json.
type ExecAdapter struct {
	executable        string
	configuredVersion string
	supportedVersions []string
	factory           ExecProcessFactory
	evidence          EvidenceRecorder
	recovery          ExecRecoveryStore
	clock             func() time.Time

	mu       sync.Mutex
	version  string
	attempts map[string]*execAttempt
}

type execAttempt struct {
	mu        sync.Mutex
	ready     chan struct{}
	changed   chan struct{}
	done      chan struct{}
	readyOnce sync.Once

	attemptID       string
	operation       attemptOperation
	operationDigest string
	initialSequence uint64
	request         providerport.AttemptRequest
	validator       attemptOutputValidator
	process         ExecProcess
	handle          providerport.AttemptHandle
	version         string
	schemaPath      string
	startErr        error

	events           []providerport.Event
	sequence         uint64
	result           providerport.AttemptResult
	terminal         bool
	threadStarted    bool
	turnStarted      bool
	turnCompleted    bool
	latestOutput     json.RawMessage
	usage            providerport.Usage
	evidenceRecords  []providerport.Evidence
	terminalEvidence string
	stderr           string
	stop             execStopCause
}

type execStopCause string

const (
	execStopNone      execStopCause = ""
	execStopCancelled execStopCause = "cancelled"
	execStopTimeout   execStopCause = "timeout"
)

type ownedExecProcess struct {
	owner  *commandOwner
	stdout io.Reader
	stderr io.Reader
}

func (process *ownedExecProcess) Stdout() io.Reader { return process.stdout }
func (process *ownedExecProcess) Stderr() io.Reader { return process.stderr }
func (process *ownedExecProcess) PID() int          { return process.owner.PID() }
func (process *ownedExecProcess) Wait() error       { return process.owner.Wait() }
func (process *ownedExecProcess) Kill() error       { return process.owner.Kill() }

var _ providerport.Provider = (*ExecAdapter)(nil)

// NewExecAdapter constructs the bounded exec fallback. Production use supplies
// an executable; tests may inject a factory and observed provider version.
func NewExecAdapter(options ExecAdapterOptions) (*ExecAdapter, error) {
	if options.EvidenceRecorder == nil {
		return nil, errors.New("codex exec adapter evidence recorder is required")
	}
	if options.RecoveryStore == nil {
		return nil, errors.New("codex exec adapter durable recovery store is required")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	versions := append([]string(nil), options.SupportedVersions...)
	if len(versions) == 0 {
		versions = append([]string(nil), defaultSupportedExecVersions...)
	}
	for index, version := range versions {
		versions[index] = strings.TrimSpace(version)
		if versions[index] == "" {
			return nil, errors.New("codex exec supported version cannot be empty")
		}
	}
	sort.Strings(versions)

	executable := strings.TrimSpace(options.Executable)
	if options.Factory == nil {
		if executable == "" {
			return nil, errors.New("codex exec adapter executable is required")
		}
		canonical, err := canonicalExecutable(executable)
		if err != nil {
			return nil, err
		}
		executable = canonical
		options.Factory = startOwnedExecProcess
	} else if strings.TrimSpace(options.ProviderVersion) == "" {
		return nil, errors.New("injected codex exec factory requires provider version")
	}
	return &ExecAdapter{
		executable: executable, configuredVersion: strings.TrimSpace(options.ProviderVersion),
		supportedVersions: versions, factory: options.Factory, evidence: options.EvidenceRecorder,
		recovery: options.RecoveryStore, clock: options.Clock, attempts: make(map[string]*execAttempt),
	}, nil
}

func startOwnedExecProcess(command ExecCommand) (ExecProcess, error) {
	process := exec.Command(command.Executable, command.Arguments...)
	process.Dir = command.Directory
	configureAppServerProcess(process)
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex exec stdout: %w", err)
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("open Codex exec stderr: %w", err)
	}
	if err := process.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("start Codex exec: %w", err)
	}
	owner, err := newCommandOwner(process)
	if err != nil {
		_ = process.Process.Kill()
		_ = process.Wait()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("own Codex exec process tree: %w", err)
	}
	return &ownedExecProcess{owner: owner, stdout: stdout, stderr: stderr}, nil
}

func (adapter *ExecAdapter) ProbeHealth(ctx context.Context) (providerport.Health, error) {
	version, err := adapter.providerVersion(ctx)
	if err != nil {
		return providerport.Health{State: providerport.HealthUnavailable, Provider: providerName, ExecutableIdentity: adapter.executable, Diagnostics: []string{err.Error()}}, nil
	}
	state := providerport.HealthAvailable
	diagnostics := []string(nil)
	if !adapter.supports(version) {
		state = providerport.HealthDegraded
		diagnostics = []string{fmt.Sprintf("codex exec JSONL is not admitted for exact version %s", version)}
	}
	return providerport.Health{State: state, Provider: providerName, ProviderVersion: version, ExecutableIdentity: adapter.executable, Platform: "windows", Diagnostics: diagnostics}, nil
}

func (adapter *ExecAdapter) Capabilities(ctx context.Context) (providerport.CapabilityManifest, error) {
	version, err := adapter.providerVersion(ctx)
	if err != nil {
		return providerport.CapabilityManifest{}, classifyAdapterError(err)
	}
	features := map[string]providerport.Capability{
		"exec_json":         providerport.AvailableCapability{Version: version, Metadata: map[string]string{"transport": execTransport, "fallback": "true"}},
		"structured_output": providerport.AvailableCapability{Version: "json-schema"},
		"resume":            providerport.AvailableCapability{Version: "session-id"},
		"interactions":      providerport.UnavailableCapability{Reason: "exec JSONL has no bidirectional interaction bridge"},
		"workspace_write":   providerport.UnavailableCapability{Reason: "bounded exec fallback is read-only"},
	}
	if !adapter.supports(version) {
		features["exec_json"] = providerport.UnavailableCapability{Reason: "exact Codex CLI version has not passed exec probes"}
	}
	digest := sha256.Sum256([]byte("exec_json=" + version + "|interactions=none|resume=session-id|structured_output=json-schema|workspace_write=none"))
	return providerport.CapabilityManifest{Provider: providerName, Fingerprint: hex.EncodeToString(digest[:]), Features: features, ObservedAt: adapter.clock().UTC()}, nil
}

func (adapter *ExecAdapter) providerVersion(ctx context.Context) (string, error) {
	adapter.mu.Lock()
	if adapter.version != "" {
		version := adapter.version
		adapter.mu.Unlock()
		return version, nil
	}
	if adapter.configuredVersion != "" {
		adapter.version = normalizeCodexVersion(adapter.configuredVersion)
		version := adapter.version
		adapter.mu.Unlock()
		return version, nil
	}
	adapter.mu.Unlock()

	command := exec.CommandContext(ctx, adapter.executable, "--version")
	configureProbeProcess(command)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("probe Codex exec version: %w", err)
	}
	version := normalizeCodexVersion(string(output))
	if version == "" {
		return "", errors.New("codex version output omitted an exact version")
	}
	adapter.mu.Lock()
	adapter.version = version
	adapter.mu.Unlock()
	return version, nil
}

func normalizeCodexVersion(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func (adapter *ExecAdapter) supports(version string) bool {
	index := sort.SearchStrings(adapter.supportedVersions, version)
	return index < len(adapter.supportedVersions) && adapter.supportedVersions[index] == version
}

func (adapter *ExecAdapter) StartAttempt(ctx context.Context, request providerport.AttemptRequest) (providerport.AttemptHandle, error) {
	normalized, validator, err := validateAttemptRequest(request)
	if err != nil {
		return providerport.AttemptHandle{}, err
	}
	if err := validateExecEligibility(normalized); err != nil {
		return providerport.AttemptHandle{}, err
	}
	digest, err := operationDigest(attemptOperationStart, normalized)
	if err != nil {
		return providerport.AttemptHandle{}, adapterFailure(ports.FailureInternal, "Codex exec start request could not be fingerprinted", false)
	}
	return adapter.begin(ctx, attemptOperationStart, digest, normalized, schemaOutputValidator{schema: validator}, providerport.ResumeRequest{})
}

func validateExecEligibility(request providerport.AttemptRequest) error {
	for _, input := range request.Inputs {
		if input.Kind == providerport.InputImage || input.Kind == providerport.InputSkill {
			return adapterFailure(ports.FailureUnsupported, "Codex exec fallback does not support image or skill inputs", false)
		}
	}
	switch {
	case request.Access != providerport.AccessReadOnly:
		return adapterFailure(ports.FailureUnsupported, "Codex exec fallback is limited to read-only nodes", false)
	case request.CommandPolicy != providerport.InteractionDeny || request.FilePolicy != providerport.InteractionDeny || request.ToolPolicy != providerport.InteractionDeny:
		return adapterFailure(ports.FailureUnsupported, "Codex exec fallback requires denied interaction policies", false)
	case request.Timeout <= 0:
		return adapterFailure(ports.FailureInvalidRequest, "Codex exec fallback requires a positive timeout", false)
	case len(request.AdditionalRoots) != 0:
		return adapterFailure(ports.FailureUnsupported, "Codex exec fallback does not admit additional workspace roots", false)
	case request.UsageLimits.InputTokens > 0 || request.UsageLimits.OutputTokens > 0 || request.UsageLimits.CostUnits > 0:
		return adapterFailure(ports.FailureUnsupported, "Codex exec fallback cannot enforce usage limits", false)
	}
	return nil
}

func (adapter *ExecAdapter) ResumeAttempt(ctx context.Context, request providerport.ResumeRequest) (providerport.AttemptHandle, error) {
	if err := validateResumeRequest(request); err != nil {
		return providerport.AttemptHandle{}, err
	}
	if request.ProviderTurnID != execSyntheticTurnID {
		return providerport.AttemptHandle{}, adapterFailure(ports.FailureUnsupported, "recorded attempt was not created by Codex exec JSONL", false)
	}
	digest, err := operationDigest(attemptOperationResume, request)
	if err != nil {
		return providerport.AttemptHandle{}, adapterFailure(ports.FailureInternal, "Codex exec resume request could not be fingerprinted", false)
	}

	adapter.mu.Lock()
	existing := adapter.attempts[request.AttemptID]
	adapter.mu.Unlock()
	var original providerport.AttemptRequest
	if existing != nil {
		existing.mu.Lock()
		original = existing.request
		existing.mu.Unlock()
	} else {
		if adapter.recovery == nil {
			return providerport.AttemptHandle{}, adapterFailure(ports.FailureUncertain, "Codex exec resume requires durable recovery request material", false)
		}
		record, loadErr := adapter.recovery.LoadExecRecovery(ctx, request.AttemptID)
		if loadErr != nil {
			return providerport.AttemptHandle{}, adapterFailure(ports.FailureUncertain, "Codex exec recovery request material could not be loaded", false)
		}
		if record.AttemptID != request.AttemptID {
			return providerport.AttemptHandle{}, adapterFailure(ports.FailureUncertain, "Codex exec recovery request identity does not match the attempt", false)
		}
		original = providerport.AttemptRequest{
			AttemptID: record.AttemptID, Workspace: record.Workspace, Prompt: record.Prompt,
			Inputs: append([]providerport.Input(nil), record.Inputs...), OutputSchema: cloneRaw(record.OutputSchema),
			Access: providerport.AccessReadOnly, Network: providerport.NetworkDenied,
			CommandPolicy: providerport.InteractionDeny, FilePolicy: providerport.InteractionDeny, ToolPolicy: providerport.InteractionDeny,
			Timeout: time.Minute,
		}
	}
	_, validator, err := validateAttemptRequest(withResumeDefaults(original))
	if err != nil {
		return providerport.AttemptHandle{}, adapterFailure(ports.FailureUncertain, "Codex exec recovery request material is invalid", false)
	}
	return adapter.begin(ctx, attemptOperationResume, digest, original, schemaOutputValidator{schema: validator}, request)
}

func withResumeDefaults(request providerport.AttemptRequest) providerport.AttemptRequest {
	if request.RunID == "" {
		request.RunID = "resume"
	}
	if request.NodeID == "" {
		request.NodeID = "resume"
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = "resume"
	}
	if request.Timeout <= 0 {
		request.Timeout = time.Minute
	}
	return request
}

func (adapter *ExecAdapter) begin(
	ctx context.Context,
	operation attemptOperation,
	digest string,
	request providerport.AttemptRequest,
	validator attemptOutputValidator,
	resume providerport.ResumeRequest,
) (providerport.AttemptHandle, error) {
	if err := ctx.Err(); err != nil {
		return providerport.AttemptHandle{}, err
	}
	version, err := adapter.providerVersion(ctx)
	if err != nil {
		return providerport.AttemptHandle{}, classifyAdapterError(err)
	}
	if !adapter.supports(version) {
		return providerport.AttemptHandle{}, adapterFailure(ports.FailureProtocolDrift, "Codex exec fallback is not admitted for the installed exact CLI version", false)
	}

	adapter.mu.Lock()
	if existing := adapter.attempts[request.AttemptID]; existing != nil {
		adapter.mu.Unlock()
		existing.mu.Lock()
		if existing.operation != operation || existing.operationDigest != digest {
			existing.mu.Unlock()
			return providerport.AttemptHandle{}, adapterFailure(ports.FailureConflict, "attempt ID was already used with a different Codex exec request", false)
		}
		existing.mu.Unlock()
		return waitForExecStart(ctx, existing)
	}
	state := &execAttempt{
		ready: make(chan struct{}), changed: make(chan struct{}), done: make(chan struct{}), attemptID: request.AttemptID,
		operation: operation, operationDigest: digest, initialSequence: resume.LastSequence,
		request: request, validator: validator, version: version, sequence: resume.LastSequence,
	}
	adapter.attempts[request.AttemptID] = state
	adapter.mu.Unlock()

	if operation == attemptOperationStart && adapter.recovery != nil {
		record := ExecRecoveryRecord{AttemptID: request.AttemptID, Workspace: request.Workspace, Prompt: request.Prompt, Inputs: append([]providerport.Input(nil), request.Inputs...), OutputSchema: cloneRaw(request.OutputSchema)}
		if err := adapter.recovery.SaveExecRecovery(ctx, record); err != nil {
			failure := adapterFailure(ports.FailureInternal, "Codex exec recovery request material could not be persisted", false)
			failExecStart(state, failure)
			adapter.removeAttempt(request.AttemptID, state)
			return providerport.AttemptHandle{}, failure
		}
	}

	schemaPath, err := writeExecSchema(request.OutputSchema)
	if err != nil {
		failure := adapterFailure(ports.FailureInternal, "Codex exec output schema could not be prepared", false)
		failExecStart(state, failure)
		adapter.removeAttempt(request.AttemptID, state)
		return providerport.AttemptHandle{}, failure
	}
	state.schemaPath = schemaPath
	if err := adapter.recordSelection(ctx, state, operation); err != nil {
		_ = os.Remove(schemaPath)
		failExecStart(state, err)
		adapter.removeAttempt(request.AttemptID, state)
		return providerport.AttemptHandle{}, err
	}
	command := adapter.execCommand(state, resume)
	process, err := adapter.factory(command)
	if err != nil {
		_ = os.Remove(schemaPath)
		failure := classifyAdapterError(err)
		failExecStart(state, failure)
		adapter.removeAttempt(request.AttemptID, state)
		return providerport.AttemptHandle{}, failure
	}
	threadID := resume.ProviderThreadID
	state.process = process
	state.handle = providerport.AttemptHandle{
		AttemptID: request.AttemptID, Provider: providerName, ProviderThreadID: threadID,
		ProviderTurnID: execSyntheticTurnID, ProcessOwnerID: strconv.Itoa(process.PID()),
	}
	go adapter.pumpExec(state, resume.ProviderThreadID)
	go adapter.enforceExecTimeout(state, request.Timeout)
	handle, err := waitForExecStart(ctx, state)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		_ = process.Kill()
	}
	return handle, err
}

func waitForExecStart(ctx context.Context, state *execAttempt) (providerport.AttemptHandle, error) {
	select {
	case <-ctx.Done():
		return providerport.AttemptHandle{}, ctx.Err()
	case <-state.ready:
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.startErr != nil {
			return providerport.AttemptHandle{}, state.startErr
		}
		if !state.threadStarted {
			if failed, ok := state.result.(providerport.FailedResult); ok {
				failure := failed.Failure
				return providerport.AttemptHandle{}, &failure
			}
			return providerport.AttemptHandle{}, adapterFailure(ports.FailureProtocolDrift, "Codex exec did not establish a session identity", false)
		}
		return state.handle, nil
	}
}

func failExecStart(state *execAttempt, err error) {
	state.mu.Lock()
	if state.startErr == nil {
		state.startErr = err
		state.terminal = true
		close(state.done)
		state.readyOnce.Do(func() { close(state.ready) })
		signalExecAttempt(state)
	}
	state.mu.Unlock()
}

func (adapter *ExecAdapter) removeAttempt(attemptID string, state *execAttempt) {
	adapter.mu.Lock()
	if adapter.attempts[attemptID] == state {
		delete(adapter.attempts, attemptID)
	}
	adapter.mu.Unlock()
}

func writeExecSchema(schema json.RawMessage) (string, error) {
	file, err := os.CreateTemp("", "darkstar-codex-output-*.schema.json")
	if err != nil {
		return "", err
	}
	path := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(schema); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	committed = true
	return path, nil
}

func (adapter *ExecAdapter) recordSelection(ctx context.Context, state *execAttempt, operation attemptOperation) error {
	payload, _ := json.Marshal(map[string]any{
		"provider": providerName, "transport": execTransport, "fallback": true,
		"reason": "bounded_non_interactive", "operation": operation, "providerVersion": state.version,
	})
	evidence, err := adapter.evidence.Record(ctx, EvidenceRecord{AttemptID: state.attemptID, Sequence: state.sequence + 1, Kind: "transport-selection", MediaType: "application/json", Data: payload})
	if err != nil {
		return adapterFailure(ports.FailureInternal, "Codex exec transport selection evidence could not be persisted", false)
	}
	state.evidenceRecords = append(state.evidenceRecords, evidence)
	return nil
}

func (adapter *ExecAdapter) execCommand(state *execAttempt, resume providerport.ResumeRequest) ExecCommand {
	prompt := buildExecPrompt(state.request)
	arguments := []string{"exec"}
	if state.operation == attemptOperationResume {
		arguments = append(arguments, "resume", "--json", "--skip-git-repo-check", "--output-schema", state.schemaPath, resume.ProviderThreadID,
			"Continue the interrupted DARKSTAR attempt. Complete the original request and return only the requested structured result.")
	} else {
		arguments = append(arguments, "--json", "--skip-git-repo-check", "--sandbox", "read-only", "--config", `approval_policy="never"`, "--output-schema", state.schemaPath, "--cd", state.request.Workspace)
		if state.request.ModelHint != "" {
			arguments = append(arguments, "--model", state.request.ModelHint)
		}
		if state.request.ReasoningHint != "" {
			arguments = append(arguments, "--config", `model_reasoning_effort="`+state.request.ReasoningHint+`"`)
		}
		arguments = append(arguments, prompt)
	}
	return ExecCommand{Executable: adapter.executable, Arguments: arguments, Directory: state.request.Workspace}
}

func buildExecPrompt(request providerport.AttemptRequest) string {
	var builder strings.Builder
	builder.WriteString(request.Prompt)
	for _, input := range request.Inputs {
		builder.WriteString("\n\n")
		if input.Name != "" {
			builder.WriteString(input.Name)
			builder.WriteString(":\n")
		}
		builder.WriteString(input.Text)
	}
	return builder.String()
}

func (adapter *ExecAdapter) pumpExec(state *execAttempt, expectedThreadID string) {
	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(state.process.Stderr(), 256*1024))
		stderrDone <- string(data)
	}()

	scanner := bufio.NewScanner(state.process.Stdout())
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var pumpErr error
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := adapter.observeExecLine(state, line, expectedThreadID); err != nil {
			pumpErr = err
			_ = state.process.Kill()
			break
		}
	}
	if pumpErr == nil {
		pumpErr = scanner.Err()
	}
	waitErr := state.process.Wait()
	state.stderr = <-stderrDone
	_ = os.Remove(state.schemaPath)
	adapter.finishExec(state, pumpErr, waitErr)
}

func (adapter *ExecAdapter) observeExecLine(state *execAttempt, line []byte, expectedThreadID string) error {
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(line, &frame); err != nil || frame == nil {
		return errors.New("codex exec emitted malformed JSONL")
	}
	typeName := rawString(frame["type"])
	if typeName == "" {
		return errors.New("codex exec JSONL frame omitted type")
	}
	state.mu.Lock()
	sequence := state.sequence + 1
	state.mu.Unlock()
	evidence, err := adapter.evidence.Record(context.Background(), EvidenceRecord{AttemptID: state.attemptID, Sequence: sequence, Kind: "exec-" + typeName, MediaType: "application/json", Data: append(line, '\n')})
	if err != nil {
		return fmt.Errorf("persist Codex exec frame: %w", err)
	}

	threadID := rawString(frame["thread_id"])
	if threadID == "" {
		threadID = rawString(frame["threadId"])
	}
	itemID, kind := execFrameKind(typeName, frame)
	payload, err := json.Marshal(struct {
		ProviderMethod string                     `json:"providerMethod"`
		Transport      string                     `json:"transport"`
		Fallback       bool                       `json:"fallback"`
		Params         map[string]json.RawMessage `json:"params"`
	}{ProviderMethod: typeName, Transport: execTransport, Fallback: true, Params: frame})
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if typeName == "thread.started" {
		if threadID == "" {
			return errors.New("codex exec thread.started omitted thread ID")
		}
		if expectedThreadID != "" && threadID != expectedThreadID {
			return errors.New("codex exec resume changed session identity")
		}
		state.threadStarted = true
		state.handle.ProviderThreadID = threadID
		state.readyOnce.Do(func() { close(state.ready) })
	}
	if typeName == "turn.started" {
		state.turnStarted = true
	}
	if typeName == "turn.completed" {
		state.turnCompleted = true
		state.usage = execUsage(frame["usage"])
	}
	if typeName == "item.completed" {
		if output, ok := execAgentOutput(frame["item"]); ok {
			state.latestOutput = output
		}
	}
	state.sequence++
	event := providerport.Event{
		SchemaVersion: 1, AttemptID: state.attemptID, Sequence: state.sequence, OccurredAt: adapter.clock().UTC(), Kind: kind,
		Provider: providerName, ProviderVersion: state.version, ProviderThreadID: state.handle.ProviderThreadID,
		ProviderTurnID: execSyntheticTurnID, ProviderItemID: itemID, Payload: payload, RawEvidenceRef: evidence.Ref,
	}
	if err := event.Validate(); err != nil {
		return err
	}
	state.events = append(state.events, event)
	state.terminalEvidence = evidence.Ref
	signalExecAttempt(state)
	return nil
}

func execFrameKind(typeName string, frame map[string]json.RawMessage) (string, providerport.EventKind) {
	switch typeName {
	case "thread.started":
		return "", providerport.EventAttemptStarted
	case "turn.started":
		return "", providerport.EventTurnStarted
	case "turn.completed":
		return "", providerport.EventTurnCompleted
	case "error":
		return "", providerport.EventError
	case "warning":
		return "", providerport.EventWarning
	case "item.started", "item.completed":
		var item map[string]json.RawMessage
		if json.Unmarshal(frame["item"], &item) != nil {
			return "", providerport.EventUnknownProvider
		}
		itemID := rawString(item["id"])
		started := typeName == "item.started"
		switch strings.ToLower(strings.ReplaceAll(rawString(item["type"]), "_", "")) {
		case "agentmessage":
			if started {
				return itemID, providerport.EventUnknownProvider
			}
			return itemID, providerport.EventMessageCompleted
		case "commandexecution":
			if started {
				return itemID, providerport.EventCommandStarted
			}
			return itemID, providerport.EventCommandCompleted
		case "filechange":
			if started {
				return itemID, providerport.EventFileChangeStarted
			}
			return itemID, providerport.EventFileChangeCompleted
		case "mcptoolcall", "dynamictoolcall", "websearch":
			if started {
				return itemID, providerport.EventToolStarted
			}
			return itemID, providerport.EventToolCompleted
		default:
			return itemID, providerport.EventUnknownProvider
		}
	default:
		return "", providerport.EventUnknownProvider
	}
}

func execAgentOutput(raw json.RawMessage) (json.RawMessage, bool) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil {
		return nil, false
	}
	itemType := strings.ToLower(strings.ReplaceAll(rawString(item["type"]), "_", ""))
	if itemType != "agentmessage" {
		return nil, false
	}
	text := rawString(item["text"])
	if !json.Valid([]byte(text)) {
		return json.RawMessage(text), true
	}
	return json.RawMessage(text), true
}

func execUsage(raw json.RawMessage) providerport.Usage {
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	return providerport.Usage{InputTokens: rawInt64(values["input_tokens"]), CachedTokens: rawInt64(values["cached_input_tokens"]), OutputTokens: rawInt64(values["output_tokens"])}
}

func (adapter *ExecAdapter) finishExec(state *execAttempt, pumpErr, waitErr error) {
	state.mu.Lock()
	if state.terminal {
		state.mu.Unlock()
		return
	}
	metadata := adapter.execMetadataLocked(state)
	stop := state.stop
	stderr := state.stderr
	boundaries := state.threadStarted && state.turnStarted && state.turnCompleted
	output := cloneRaw(state.latestOutput)
	state.mu.Unlock()

	var result providerport.AttemptResult
	switch {
	case stop == execStopCancelled:
		result = providerport.CancelledResult{AttemptResultMetadata: metadata}
	case stop == execStopTimeout:
		result = providerport.InterruptedResult{AttemptResultMetadata: metadata, Failure: ports.Failure{Code: ports.FailureTimeout, Message: "Codex exec attempt exceeded its timeout", Retryable: true}}
	case pumpErr != nil:
		result = providerport.FailedResult{AttemptResultMetadata: metadata, Failure: ports.Failure{Code: ports.FailureProtocolDrift, Message: pumpErr.Error(), Retryable: false}}
	case waitErr != nil:
		failure := classifyExecExit(waitErr, stderr)
		result = providerport.FailedResult{AttemptResultMetadata: metadata, Failure: failure}
	case !boundaries:
		result = providerport.FailedResult{AttemptResultMetadata: metadata, Failure: ports.Failure{Code: ports.FailureProtocolDrift, Message: "Codex exec omitted required JSONL lifecycle boundaries", Retryable: false}}
	case len(bytes.TrimSpace(output)) == 0 || !json.Valid(output):
		result = providerport.FailedResult{AttemptResultMetadata: metadata, Failure: ports.Failure{Code: ports.FailureProtocolDrift, Message: "Codex exec omitted valid structured output", Retryable: false}}
	case state.validator.validate(output) != nil:
		result = providerport.FailedResult{AttemptResultMetadata: metadata, Failure: ports.Failure{Code: ports.FailureInvalidRequest, Message: "Codex exec structured output failed schema validation", Retryable: false}}
	default:
		if err := adapter.appendExecDerivedOutput(state, output); err != nil {
			result = providerport.FailedResult{AttemptResultMetadata: metadata, Failure: ports.Failure{Code: ports.FailureInternal, Message: "Codex exec structured output evidence could not be persisted", Retryable: false}}
		} else {
			state.mu.Lock()
			metadata = adapter.execMetadataLocked(state)
			state.mu.Unlock()
			result = providerport.SucceededResult{AttemptResultMetadata: metadata, StructuredOutput: output}
		}
	}
	state.mu.Lock()
	if !state.terminal {
		state.result = result
		state.terminal = true
		close(state.done)
		state.readyOnce.Do(func() { close(state.ready) })
		signalExecAttempt(state)
	}
	state.mu.Unlock()
}

func classifyExecExit(waitErr error, stderr string) ports.Failure {
	message := strings.ToLower(stderr)
	details := map[string]string{}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		details["exitCode"] = strconv.Itoa(exitErr.ExitCode())
	}
	switch {
	case strings.Contains(message, "unexpected argument") || strings.Contains(message, "usage: codex exec"):
		return ports.Failure{Code: ports.FailureProtocolDrift, Message: "Codex exec command-line contract drifted", Retryable: false, Details: details}
	case strings.Contains(message, "not logged in") || strings.Contains(message, "authentication") || strings.Contains(message, "unauthorized"):
		return ports.Failure{Code: ports.FailureUnauthenticated, Message: "Codex exec requires authentication", Retryable: false, Details: details}
	case strings.Contains(message, "rate limit") || strings.Contains(message, "usage limit") || strings.Contains(message, "quota"):
		return ports.Failure{Code: ports.FailureResourceExhausted, Message: "Codex exec usage is exhausted", Retryable: true, Details: details}
	case strings.Contains(message, "permission denied") || strings.Contains(message, "access is denied"):
		return ports.Failure{Code: ports.FailurePermissionDenied, Message: "Codex exec was denied required access", Retryable: false, Details: details}
	default:
		return ports.Failure{Code: ports.FailureUnavailable, Message: "Codex exec process failed", Retryable: true, Details: details}
	}
}

func (adapter *ExecAdapter) appendExecDerivedOutput(state *execAttempt, output json.RawMessage) error {
	payload, _ := json.Marshal(map[string]any{"output": json.RawMessage(output), "transport": execTransport})
	state.mu.Lock()
	sequence := state.sequence + 1
	state.mu.Unlock()
	evidence, err := adapter.evidence.Record(context.Background(), EvidenceRecord{
		AttemptID: state.attemptID, Sequence: sequence, Kind: string(providerport.EventStructuredOutputCompleted), MediaType: "application/json", Data: output,
	})
	if err != nil {
		return err
	}
	state.mu.Lock()
	state.sequence = sequence
	event := providerport.Event{
		SchemaVersion: 1, AttemptID: state.attemptID, Sequence: state.sequence, OccurredAt: adapter.clock().UTC(),
		Kind: providerport.EventStructuredOutputCompleted, Provider: providerName, ProviderVersion: state.version,
		ProviderThreadID: state.handle.ProviderThreadID, ProviderTurnID: execSyntheticTurnID, Payload: payload, RawEvidenceRef: evidence.Ref,
	}
	state.events = append(state.events, event)
	state.evidenceRecords = append(state.evidenceRecords, evidence)
	state.terminalEvidence = evidence.Ref
	signalExecAttempt(state)
	state.mu.Unlock()
	return nil
}

func (adapter *ExecAdapter) enforceExecTimeout(state *execAttempt, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-state.done:
		return
	case <-timer.C:
		state.mu.Lock()
		if state.terminal || state.stop != execStopNone {
			state.mu.Unlock()
			return
		}
		state.stop = execStopTimeout
		process := state.process
		state.mu.Unlock()
		_ = process.Kill()
	}
}

func (adapter *ExecAdapter) StreamEvents(ctx context.Context, request providerport.EventRequest) (providerport.EventStream, error) {
	state, err := adapter.execAttempt(request.Handle.AttemptID)
	if err != nil {
		return nil, err
	}
	if err := validateExecHandle(state, request.Handle); err != nil {
		return nil, err
	}
	return &execEventStream{ctx: ctx, state: state, after: request.AfterSequence, closed: make(chan struct{})}, nil
}

func (adapter *ExecAdapter) Respond(context.Context, providerport.InteractionResponse) (providerport.InteractionReceipt, error) {
	return providerport.InteractionReceipt{}, adapterFailure(ports.FailureUnsupported, "Codex exec fallback does not support interactions", false)
}

func (adapter *ExecAdapter) CancelAttempt(ctx context.Context, request providerport.CancelRequest) (providerport.CancelResult, error) {
	if err := ctx.Err(); err != nil {
		return providerport.CancelResult{}, err
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return providerport.CancelResult{}, adapterFailure(ports.FailureInvalidRequest, "cancel idempotency key is required", false)
	}
	state, err := adapter.execAttempt(request.Handle.AttemptID)
	if err != nil {
		return providerport.CancelResult{}, err
	}
	if err := validateExecHandle(state, request.Handle); err != nil {
		return providerport.CancelResult{}, err
	}
	state.mu.Lock()
	if state.terminal {
		state.mu.Unlock()
		return providerport.CancelResult{Disposition: providerport.CancelAlreadyDone, EvidenceRef: state.terminalEvidence}, nil
	}
	if state.stop == execStopNone {
		state.stop = execStopCancelled
	}
	process := state.process
	state.mu.Unlock()
	if err := process.Kill(); err != nil {
		return providerport.CancelResult{Disposition: providerport.CancelUncertain}, adapterFailure(ports.FailureUncertain, "Codex exec process tree termination could not be confirmed", false)
	}
	payload, _ := json.Marshal(map[string]any{"transport": execTransport, "disposition": providerport.CancelForced})
	state.mu.Lock()
	evidenceSequence := state.sequence + 1
	state.mu.Unlock()
	evidence, recordErr := adapter.evidence.Record(ctx, EvidenceRecord{AttemptID: state.attemptID, Sequence: evidenceSequence, Kind: "exec-cancel", MediaType: "application/json", Data: payload})
	if recordErr != nil {
		return providerport.CancelResult{}, adapterFailure(ports.FailureInternal, "Codex exec cancellation evidence could not be persisted", false)
	}
	state.mu.Lock()
	state.terminalEvidence = evidence.Ref
	state.mu.Unlock()
	return providerport.CancelResult{Disposition: providerport.CancelForced, EvidenceRef: evidence.Ref}, nil
}

func (adapter *ExecAdapter) GetResult(ctx context.Context, request providerport.ResultRequest) (providerport.AttemptResult, error) {
	state, err := adapter.execAttempt(request.Handle.AttemptID)
	if err != nil {
		return nil, err
	}
	if err := validateExecHandle(state, request.Handle); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-state.done:
		state.mu.Lock()
		defer state.mu.Unlock()
		return cloneAttemptResult(state.result), nil
	}
}

func (adapter *ExecAdapter) execMetadataLocked(state *execAttempt) providerport.AttemptResultMetadata {
	return providerport.AttemptResultMetadata{
		Usage: state.usage, WorkspaceEvidence: append([]providerport.Evidence(nil), state.evidenceRecords...),
		Recovery: providerport.RecoveryMetadata{
			ProviderThreadID: state.handle.ProviderThreadID, ProviderTurnID: execSyntheticTurnID,
			LastSequence: state.sequence, ProcessOwnerID: state.handle.ProcessOwnerID,
			Resumable: state.handle.ProviderThreadID != "", EvidenceRef: state.terminalEvidence,
		},
	}
}

func (adapter *ExecAdapter) execAttempt(attemptID string) (*execAttempt, error) {
	adapter.mu.Lock()
	state := adapter.attempts[attemptID]
	adapter.mu.Unlock()
	if state == nil {
		return nil, adapterFailure(ports.FailureNotFound, "Codex exec attempt was not found", false)
	}
	return state, nil
}

func validateExecHandle(state *execAttempt, handle providerport.AttemptHandle) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if handle.AttemptID != state.handle.AttemptID || handle.Provider != providerName || handle.ProviderTurnID != execSyntheticTurnID || handle.ProcessOwnerID != state.handle.ProcessOwnerID {
		return adapterFailure(ports.FailureConflict, "Codex exec attempt handle does not match the owned process", false)
	}
	if state.handle.ProviderThreadID != "" && handle.ProviderThreadID != state.handle.ProviderThreadID {
		return adapterFailure(ports.FailureConflict, "Codex exec attempt handle changed session identity", false)
	}
	return nil
}

type execEventStream struct {
	ctx    context.Context
	state  *execAttempt
	after  uint64
	closed chan struct{}
	once   sync.Once
}

func (stream *execEventStream) Receive() (providerport.Event, error) {
	for {
		stream.state.mu.Lock()
		for _, event := range stream.state.events {
			if event.Sequence > stream.after {
				stream.after = event.Sequence
				stream.state.mu.Unlock()
				return cloneEvent(event), nil
			}
		}
		if stream.state.terminal {
			stream.state.mu.Unlock()
			return providerport.Event{}, io.EOF
		}
		changed := stream.state.changed
		stream.state.mu.Unlock()
		select {
		case <-stream.ctx.Done():
			return providerport.Event{}, stream.ctx.Err()
		case <-stream.closed:
			return providerport.Event{}, io.EOF
		case <-changed:
		}
	}
}

func (stream *execEventStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return nil
}

func signalExecAttempt(state *execAttempt) {
	close(state.changed)
	state.changed = make(chan struct{})
}

// MemoryExecRecoveryStore is a concurrency-safe helper for embedded callers
// and tests. Daemons should use a durable store implementation.
type MemoryExecRecoveryStore struct {
	mu      sync.Mutex
	records map[string]ExecRecoveryRecord
}

func NewMemoryExecRecoveryStore() *MemoryExecRecoveryStore {
	return &MemoryExecRecoveryStore{records: make(map[string]ExecRecoveryRecord)}
}

func (store *MemoryExecRecoveryStore) SaveExecRecovery(_ context.Context, record ExecRecoveryRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record.Inputs = append([]providerport.Input(nil), record.Inputs...)
	record.OutputSchema = cloneRaw(record.OutputSchema)
	store.records[record.AttemptID] = record
	return nil
}

func (store *MemoryExecRecoveryStore) LoadExecRecovery(_ context.Context, attemptID string) (ExecRecoveryRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[attemptID]
	if !ok {
		return ExecRecoveryRecord{}, os.ErrNotExist
	}
	record.Inputs = append([]providerport.Input(nil), record.Inputs...)
	record.OutputSchema = cloneRaw(record.OutputSchema)
	return record, nil
}
