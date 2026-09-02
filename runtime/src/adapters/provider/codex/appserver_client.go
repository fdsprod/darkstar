// Package codex implements the Codex provider adapter.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxMessageBytes = 16 << 20
	defaultShutdownTimeout = 5 * time.Second
)

var supportedAppServerVersions = []string{
	"0.151.0-alpha.7.1",
	"0.151.0-alpha.7.2",
}

// ClientInfo identifies DARKSTAR during the App Server initialize handshake.
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// AppServerOptions configures protocol negotiation and framing limits.
type AppServerOptions struct {
	ClientInfo        ClientInfo
	SupportedVersions []string
	MaxMessageBytes   int
}

// InitializeResult is the required, stable portion of initialize response.
type InitializeResult struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

// RPCError is a safe representation of a JSON-RPC error response.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (err *RPCError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("codex app-server RPC %d: %s", err.Code, err.Message)
}

// UnsupportedVersionError reports an initialized but unreviewed wire version.
type UnsupportedVersionError struct {
	Observed  string
	Supported []string
}

func (err *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported Codex App Server version %q (supported: %s)", err.Observed, strings.Join(err.Supported, ", "))
}

// ServerNotification is a server-to-client message without a request ID.
type ServerNotification struct {
	Method      string
	Params      json.RawMessage
	EmittedAtMS int64
}

func (ServerNotification) isIncomingMessage() {}

// ServerRequest is an ID-bearing request that requires one correlated reply.
type ServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

func (ServerRequest) isIncomingMessage() {}

// IncomingMessage is the closed request/notification union in original wire
// order. The typed projection channels remain available for direct consumers.
type IncomingMessage interface{ isIncomingMessage() }

// ThreadRef and TurnRef retain the opaque provider identities returned by Codex.
// Resume responses also expose enough turn state to prove that the recorded
// in-progress turn is the one this client rejoined.
type ThreadRef struct {
	ID    string
	Turns []TurnRef
}

type TurnRef struct {
	ID     string
	Status string
}

type wireMessage struct {
	ID          json.RawMessage `json:"id,omitempty"`
	Method      string          `json:"method,omitempty"`
	Params      json.RawMessage `json:"params,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       *RPCError       `json:"error,omitempty"`
	EmittedAtMS int64           `json:"emittedAtMs,omitempty"`
}

type callResult struct {
	result json.RawMessage
	err    error
}

type processOwner interface {
	Wait() error
	Kill() error
	PID() int
}

// AppServerClient is a concurrency-safe, newline-delimited JSON-RPC client.
// Notifications and requests use unbounded relays so protocol traffic is never
// discarded merely because an event consumer is momentarily slower than Codex.
type AppServerClient struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	owner  processOwner

	options AppServerOptions

	writeMu sync.Mutex
	stateMu sync.Mutex
	pending map[string]chan callResult
	threads map[string]struct{}

	nextID       atomic.Uint64
	initialized  atomic.Bool
	closing      atomic.Bool
	providerVers atomic.Value

	notificationInput chan ServerNotification
	notifications     chan ServerNotification
	requestInput      chan ServerRequest
	requests          chan ServerRequest
	incomingInput     chan IncomingMessage
	incoming          chan IncomingMessage
	done              chan struct{}
	terminalOnce      sync.Once
	terminalErr       error
}

// NewAppServerClient binds the protocol client to an already-started stdio
// transport. StartAppServer is the production constructor.
func NewAppServerClient(stdin io.WriteCloser, stdout io.ReadCloser, options AppServerOptions) (*AppServerClient, error) {
	return newAppServerClient(stdin, stdout, nil, options)
}

func newAppServerClient(stdin io.WriteCloser, stdout io.ReadCloser, owner processOwner, options AppServerOptions) (*AppServerClient, error) {
	if stdin == nil || stdout == nil {
		return nil, errors.New("codex App Server stdin and stdout are required")
	}
	if strings.TrimSpace(options.ClientInfo.Name) == "" || strings.TrimSpace(options.ClientInfo.Version) == "" {
		return nil, errors.New("codex App Server client name and version are required")
	}
	if len(options.SupportedVersions) == 0 {
		options.SupportedVersions = append([]string(nil), supportedAppServerVersions...)
	} else {
		options.SupportedVersions = append([]string(nil), options.SupportedVersions...)
	}
	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultMaxMessageBytes
	}

	client := &AppServerClient{
		stdin:             stdin,
		stdout:            stdout,
		owner:             owner,
		options:           options,
		pending:           make(map[string]chan callResult),
		threads:           make(map[string]struct{}),
		notificationInput: make(chan ServerNotification),
		notifications:     make(chan ServerNotification),
		requestInput:      make(chan ServerRequest),
		requests:          make(chan ServerRequest),
		incomingInput:     make(chan IncomingMessage),
		incoming:          make(chan IncomingMessage),
		done:              make(chan struct{}),
	}
	client.providerVers.Store("")
	go relay(client.notificationInput, client.notifications)
	go relay(client.requestInput, client.requests)
	go relay(client.incomingInput, client.incoming)
	go client.readLoop()
	return client, nil
}

// Initialize performs initialize/initialized negotiation and exact-version
// admission before any thread operation is allowed.
func (client *AppServerClient) Initialize(ctx context.Context) (InitializeResult, error) {
	if client.initialized.Load() {
		return InitializeResult{}, errors.New("codex App Server client is already initialized")
	}
	params := struct {
		ClientInfo   ClientInfo      `json:"clientInfo"`
		Capabilities map[string]bool `json:"capabilities"`
	}{
		ClientInfo:   client.options.ClientInfo,
		Capabilities: map[string]bool{"experimentalApi": true},
	}
	var result InitializeResult
	if err := client.Call(ctx, "initialize", params, &result); err != nil {
		return InitializeResult{}, err
	}
	if strings.TrimSpace(result.UserAgent) == "" || strings.TrimSpace(result.PlatformFamily) == "" {
		return InitializeResult{}, errors.New("codex App Server initialize response omitted required identity fields")
	}
	version := versionFromUserAgent(result.UserAgent)
	if !contains(client.options.SupportedVersions, version) {
		return InitializeResult{}, &UnsupportedVersionError{Observed: version, Supported: append([]string(nil), client.options.SupportedVersions...)}
	}
	if err := client.Notify("initialized", struct{}{}); err != nil {
		return InitializeResult{}, err
	}
	client.providerVers.Store(version)
	client.initialized.Store(true)
	return result, nil
}

// ProviderVersion returns the exact version admitted by Initialize.
func (client *AppServerClient) ProviderVersion() string {
	value, _ := client.providerVers.Load().(string)
	return value
}

// ProcessID identifies the owned App Server process, or zero for an injected transport.
func (client *AppServerClient) ProcessID() int {
	if client.owner == nil {
		return 0
	}
	return client.owner.PID()
}

// Notifications returns every passive provider notification in wire order.
func (client *AppServerClient) Notifications() <-chan ServerNotification { return client.notifications }

// Requests returns every ID-bearing server request in wire order.
func (client *AppServerClient) Requests() <-chan ServerRequest { return client.requests }

// Messages returns server notifications and requests in their original wire order.
func (client *AppServerClient) Messages() <-chan IncomingMessage { return client.incoming }

// Call sends one JSON-RPC request and waits for its correlated response.
func (client *AppServerClient) Call(ctx context.Context, method string, params any, target any) error {
	return client.call(ctx, method, params, target, false)
}

func (client *AppServerClient) call(ctx context.Context, method string, params any, target any, allowClosing bool) error {
	if strings.TrimSpace(method) == "" {
		return errors.New("codex App Server method is required")
	}
	if client.closing.Load() && !allowClosing {
		return errors.New("codex App Server client is closing")
	}
	payload, err := marshalValue(params)
	if err != nil {
		return fmt.Errorf("encode Codex App Server %s params: %w", method, err)
	}
	id := client.nextID.Add(1)
	key := fmt.Sprintf("%d", id)
	response := make(chan callResult, 1)
	client.stateMu.Lock()
	client.pending[key] = response
	client.stateMu.Unlock()

	if err := client.write(wireMessage{ID: json.RawMessage(key), Method: method, Params: payload}); err != nil {
		client.removePending(key)
		return err
	}

	select {
	case <-ctx.Done():
		client.removePending(key)
		return ctx.Err()
	case <-client.done:
		select {
		case reply := <-response:
			return decodeCallResult(method, target, reply)
		default:
		}
		return client.protocolError()
	case reply := <-response:
		return decodeCallResult(method, target, reply)
	}
}

func decodeCallResult(method string, target any, reply callResult) error {
	if reply.err != nil {
		return reply.err
	}
	if target == nil {
		return nil
	}
	if len(reply.result) == 0 {
		return fmt.Errorf("codex App Server %s response omitted result", method)
	}
	if err := json.Unmarshal(reply.result, target); err != nil {
		return fmt.Errorf("decode Codex App Server %s result: %w", method, err)
	}
	return nil
}

// Notify sends one client-to-server notification.
func (client *AppServerClient) Notify(method string, params any) error {
	if strings.TrimSpace(method) == "" {
		return errors.New("codex App Server notification method is required")
	}
	payload, err := marshalValue(params)
	if err != nil {
		return fmt.Errorf("encode Codex App Server %s notification params: %w", method, err)
	}
	return client.write(wireMessage{Method: method, Params: payload})
}

// Respond sends a successful reply to one server-initiated request.
func (client *AppServerClient) Respond(requestID json.RawMessage, result any) error {
	if len(bytes.TrimSpace(requestID)) == 0 {
		return errors.New("codex App Server request ID is required")
	}
	payload, err := marshalValue(result)
	if err != nil {
		return fmt.Errorf("encode Codex App Server request result: %w", err)
	}
	return client.write(wireMessage{ID: cloneRaw(requestID), Result: payload})
}

// RespondError sends an error reply to one server-initiated request.
func (client *AppServerClient) RespondError(requestID json.RawMessage, rpcError RPCError) error {
	if len(bytes.TrimSpace(requestID)) == 0 {
		return errors.New("codex App Server request ID is required")
	}
	return client.write(wireMessage{ID: cloneRaw(requestID), Error: &rpcError})
}

// StartThread creates a thread and begins ownership tracking for clean shutdown.
func (client *AppServerClient) StartThread(ctx context.Context, params any) (ThreadRef, error) {
	if err := client.requireInitialized(); err != nil {
		return ThreadRef{}, err
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/start", params, &response); err != nil {
		return ThreadRef{}, err
	}
	return client.rememberThread(response.Thread.ID, "thread/start")
}

// ResumeThread resumes a provider thread and begins ownership tracking.
func (client *AppServerClient) ResumeThread(ctx context.Context, params any) (ThreadRef, error) {
	if err := client.requireInitialized(); err != nil {
		return ThreadRef{}, err
	}
	var response struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/resume", params, &response); err != nil {
		return ThreadRef{}, err
	}
	thread, err := client.rememberThread(response.Thread.ID, "thread/resume")
	if err != nil {
		return ThreadRef{}, err
	}
	thread.Turns = make([]TurnRef, len(response.Thread.Turns))
	for index, turn := range response.Thread.Turns {
		thread.Turns[index] = TurnRef{ID: turn.ID, Status: turn.Status}
	}
	return thread, nil
}

// StartTurn starts a turn on a tracked thread.
func (client *AppServerClient) StartTurn(ctx context.Context, params any) (TurnRef, error) {
	if err := client.requireInitialized(); err != nil {
		return TurnRef{}, err
	}
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := client.Call(ctx, "turn/start", params, &response); err != nil {
		return TurnRef{}, err
	}
	if strings.TrimSpace(response.Turn.ID) == "" {
		return TurnRef{}, errors.New("codex App Server turn/start response omitted turn ID")
	}
	return TurnRef{ID: response.Turn.ID}, nil
}

// InterruptTurn requests graceful interruption of an active turn.
func (client *AppServerClient) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	return client.Call(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, &struct{}{})
}

// Unsubscribe releases one thread writer before process shutdown.
func (client *AppServerClient) Unsubscribe(ctx context.Context, threadID string) error {
	return client.unsubscribe(ctx, threadID, false)
}

func (client *AppServerClient) unsubscribe(ctx context.Context, threadID string, allowClosing bool) error {
	if strings.TrimSpace(threadID) == "" {
		return errors.New("codex App Server thread ID is required")
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := client.call(ctx, "thread/unsubscribe", map[string]string{"threadId": threadID}, &response, allowClosing); err != nil {
		return err
	}
	if response.Status != "unsubscribed" {
		return fmt.Errorf("codex App Server thread/unsubscribe returned status %q", response.Status)
	}
	client.stateMu.Lock()
	delete(client.threads, threadID)
	client.stateMu.Unlock()
	return nil
}

// Shutdown performs a clean Windows-safe release: every owned thread is
// unsubscribed successfully before stdin is closed and the process is awaited.
func (client *AppServerClient) Shutdown(ctx context.Context) error {
	if !client.closing.CompareAndSwap(false, true) {
		select {
		case <-client.done:
			return client.protocolErrorOrNil()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	client.stateMu.Lock()
	threadIDs := make([]string, 0, len(client.threads))
	for threadID := range client.threads {
		threadIDs = append(threadIDs, threadID)
	}
	client.stateMu.Unlock()
	sort.Strings(threadIDs)

	for _, threadID := range threadIDs {
		if err := client.unsubscribe(ctx, threadID, true); err != nil {
			return fmt.Errorf("release Codex App Server thread %s: %w", threadID, err)
		}
	}

	if err := client.stdin.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("close Codex App Server stdin: %w", err)
	}
	if client.owner == nil {
		_ = client.stdout.Close()
		return nil
	}
	waited := make(chan error, 1)
	go func() { waited <- client.owner.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			return fmt.Errorf("wait for Codex App Server: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close gives io.Closer callers the same unsubscribe-first behavior with a
// bounded timeout. A failed clean release is returned; callers may then choose
// an explicit forced termination policy.
func (client *AppServerClient) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	return client.Shutdown(ctx)
}

// KillOwnedProcess is the explicit forced-close primitive used only after a
// higher-level cancellation grace policy expires.
func (client *AppServerClient) KillOwnedProcess() error {
	if client.owner == nil {
		return errors.New("codex App Server client does not own a process")
	}
	client.closing.Store(true)
	return client.owner.Kill()
}

func (client *AppServerClient) readLoop() {
	scanner := bufio.NewScanner(client.stdout)
	scanner.Buffer(make([]byte, 64*1024), client.options.MaxMessageBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message wireMessage
		if err := json.Unmarshal(line, &message); err != nil {
			client.terminate(fmt.Errorf("decode Codex App Server message: %w", err))
			return
		}
		if len(message.ID) > 0 && message.Method != "" {
			request := ServerRequest{ID: cloneRaw(message.ID), Method: message.Method, Params: cloneRaw(message.Params)}
			client.incomingInput <- request
			client.requestInput <- request
			continue
		}
		if len(message.ID) > 0 {
			client.deliverResponse(message)
			continue
		}
		if message.Method == "" {
			client.terminate(errors.New("codex App Server message has neither ID nor method"))
			return
		}
		notification := ServerNotification{Method: message.Method, Params: cloneRaw(message.Params), EmittedAtMS: message.EmittedAtMS}
		client.incomingInput <- notification
		client.notificationInput <- notification
	}
	if err := scanner.Err(); err != nil {
		client.terminate(fmt.Errorf("read Codex App Server stdout: %w", err))
		return
	}
	client.terminate(io.EOF)
}

func (client *AppServerClient) deliverResponse(message wireMessage) {
	key := string(bytes.TrimSpace(message.ID))
	client.stateMu.Lock()
	waiter := client.pending[key]
	delete(client.pending, key)
	client.stateMu.Unlock()
	if waiter == nil {
		return
	}
	if message.Error != nil {
		waiter <- callResult{err: message.Error}
		return
	}
	waiter <- callResult{result: cloneRaw(message.Result)}
}

func (client *AppServerClient) write(message wireMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode Codex App Server message: %w", err)
	}
	payload = append(payload, '\n')
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	if _, err := client.stdin.Write(payload); err != nil {
		return fmt.Errorf("write Codex App Server stdin: %w", err)
	}
	return nil
}

func (client *AppServerClient) terminate(err error) {
	client.terminalOnce.Do(func() {
		client.stateMu.Lock()
		client.terminalErr = err
		for key, waiter := range client.pending {
			waiter <- callResult{err: err}
			delete(client.pending, key)
		}
		client.stateMu.Unlock()
		close(client.notificationInput)
		close(client.requestInput)
		close(client.incomingInput)
		close(client.done)
	})
}

func (client *AppServerClient) protocolError() error {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	if client.terminalErr == nil {
		return io.EOF
	}
	return client.terminalErr
}

func (client *AppServerClient) protocolErrorOrNil() error {
	err := client.protocolError()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (client *AppServerClient) removePending(key string) {
	client.stateMu.Lock()
	delete(client.pending, key)
	client.stateMu.Unlock()
}

func (client *AppServerClient) requireInitialized() error {
	if !client.initialized.Load() {
		return errors.New("codex App Server client is not initialized")
	}
	return nil
}

func (client *AppServerClient) rememberThread(threadID, method string) (ThreadRef, error) {
	if strings.TrimSpace(threadID) == "" {
		return ThreadRef{}, fmt.Errorf("codex App Server %s response omitted thread ID", method)
	}
	client.stateMu.Lock()
	client.threads[threadID] = struct{}{}
	client.stateMu.Unlock()
	return ThreadRef{ID: threadID}, nil
}

func versionFromUserAgent(userAgent string) string {
	for _, field := range strings.Fields(userAgent) {
		if strings.HasPrefix(field, "Codex/") || strings.HasPrefix(field, "Desktop/") {
			return strings.TrimPrefix(strings.TrimPrefix(field, "Codex/"), "Desktop/")
		}
	}
	const marker = "Codex Desktop/"
	if index := strings.Index(userAgent, marker); index >= 0 {
		version := userAgent[index+len(marker):]
		if end := strings.IndexAny(version, " )"); end >= 0 {
			version = version[:end]
		}
		return version
	}
	return ""
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func marshalValue(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, errors.New("value is not valid JSON")
		}
		return cloneRaw(raw), nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func cloneRaw(source json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), source...)
}

func relay[T any](input <-chan T, output chan<- T) {
	defer close(output)
	queue := make([]T, 0)
	for input != nil || len(queue) > 0 {
		var send chan<- T
		var next T
		if len(queue) > 0 {
			send = output
			next = queue[0]
		}
		select {
		case value, open := <-input:
			if !open {
				input = nil
				continue
			}
			queue = append(queue, value)
		case send <- next:
			var zero T
			queue[0] = zero
			queue = queue[1:]
		}
	}
}
