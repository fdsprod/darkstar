package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type testServer struct {
	requests *bufio.Scanner
	replies  *io.PipeWriter
	close    func()
	mu       sync.Mutex
}

func newTestClient(t *testing.T, versions ...string) (*AppServerClient, *testServer) {
	t.Helper()
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	client, err := NewAppServerClient(clientWrites, clientReads, AppServerOptions{
		ClientInfo:        ClientInfo{Name: "darkstar-test", Version: "1.0.0"},
		SupportedVersions: versions,
	})
	if err != nil {
		t.Fatalf("NewAppServerClient() error = %v", err)
	}
	server := &testServer{
		requests: bufio.NewScanner(serverReads),
		replies:  serverWrites,
		close: func() {
			_ = serverReads.Close()
			_ = serverWrites.Close()
		},
	}
	t.Cleanup(server.close)
	return client, server
}

func (server *testServer) receive(t *testing.T) wireMessage {
	t.Helper()
	if !server.requests.Scan() {
		t.Fatalf("server request scan failed: %v", server.requests.Err())
	}
	var message wireMessage
	if err := json.Unmarshal(server.requests.Bytes(), &message); err != nil {
		t.Fatalf("decode client message: %v", err)
	}
	return message
}

func (server *testServer) send(t *testing.T, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode server message: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if _, err := server.replies.Write(append(payload, '\n')); err != nil {
		t.Fatalf("write server message: %v", err)
	}
}

func initializeClient(t *testing.T, client *AppServerClient, server *testServer) {
	t.Helper()
	completed := make(chan error, 1)
	go func() {
		_, err := client.Initialize(context.Background())
		completed <- err
	}()
	request := server.receive(t)
	if request.Method != "initialize" || string(request.ID) != "1" {
		t.Fatalf("initialize request = %#v", request)
	}
	server.send(t, map[string]any{
		"id": 1,
		"result": map[string]any{
			"userAgent":      "Codex Desktop/0.151.0-alpha.7.2 (Windows 10; x86_64)",
			"codexHome":      `C:\Users\test\.codex`,
			"platformFamily": "windows",
			"platformOs":     "windows",
		},
	})
	initialized := server.receive(t)
	if initialized.Method != "initialized" || len(initialized.ID) != 0 {
		t.Fatalf("initialized notification = %#v", initialized)
	}
	if err := <-completed; err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
}

func TestAppServerClientInitializesAndRejectsUnknownVersions(t *testing.T) {
	t.Parallel()

	client, server := newTestClient(t, "0.151.0-alpha.7.2")
	initializeClient(t, client, server)
	if got := client.ProviderVersion(); got != "0.151.0-alpha.7.2" {
		t.Fatalf("ProviderVersion() = %q", got)
	}

	unknown, unknownServer := newTestClient(t, "0.151.0-alpha.7.1")
	completed := make(chan error, 1)
	go func() {
		_, err := unknown.Initialize(context.Background())
		completed <- err
	}()
	_ = unknownServer.receive(t)
	unknownServer.send(t, map[string]any{
		"id":     1,
		"result": map[string]any{"userAgent": "Codex Desktop/9.9.9 (Windows)", "platformFamily": "windows"},
	})
	var versionError *UnsupportedVersionError
	if err := <-completed; !errors.As(err, &versionError) || versionError.Observed != "9.9.9" {
		t.Fatalf("Initialize() error = %#v, want unsupported 9.9.9", err)
	}
}

func TestAppServerClientCorrelatesCallsAndSeparatesRequestsFromNotifications(t *testing.T) {
	t.Parallel()

	client, server := newTestClient(t, "0.151.0-alpha.7.2")
	initializeClient(t, client, server)

	server.send(t, map[string]any{"method": "turn/started", "params": map[string]string{"threadId": "thread-1"}, "emittedAtMs": 1000})
	server.send(t, map[string]any{"id": 0, "method": "item/tool/requestUserInput", "params": map[string]any{"isBlocking": true}})

	notification := <-client.Notifications()
	if notification.Method != "turn/started" || notification.EmittedAtMS != 1000 {
		t.Fatalf("notification = %#v", notification)
	}
	request := <-client.Requests()
	if request.Method != "item/tool/requestUserInput" || string(request.ID) != "0" {
		t.Fatalf("request = %#v", request)
	}
	responded := make(chan error, 1)
	go func() { responded <- client.Respond(request.ID, map[string]any{"answers": map[string]any{}}) }()
	response := server.receive(t)
	if string(response.ID) != "0" || len(response.Result) == 0 || response.Method != "" {
		t.Fatalf("server request response = %#v", response)
	}
	if err := <-responded; err != nil {
		t.Fatalf("Respond() error = %v", err)
	}

	completed := make(chan error, 1)
	go func() {
		var target struct {
			Value string `json:"value"`
		}
		err := client.Call(context.Background(), "test/read", map[string]bool{"ok": true}, &target)
		if err == nil && target.Value != "done" {
			err = errors.New("wrong result")
		}
		completed <- err
	}()
	call := server.receive(t)
	server.send(t, map[string]any{"id": json.RawMessage(call.ID), "result": map[string]string{"value": "done"}})
	if err := <-completed; err != nil {
		t.Fatalf("Call() error = %v", err)
	}
}

func TestAppServerClientTracksThreadsTurnsAndUnsubscribesBeforeShutdown(t *testing.T) {
	t.Parallel()

	client, server := newTestClient(t, "0.151.0-alpha.7.2")
	initializeClient(t, client, server)

	threadDone := make(chan error, 1)
	go func() {
		thread, err := client.StartThread(context.Background(), map[string]any{"cwd": `C:\repo`})
		if err == nil && thread.ID != "thread-1" {
			err = errors.New("wrong thread ID")
		}
		threadDone <- err
	}()
	request := server.receive(t)
	server.send(t, map[string]any{"id": json.RawMessage(request.ID), "result": map[string]any{"thread": map[string]string{"id": "thread-1"}}})
	if err := <-threadDone; err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}

	turnDone := make(chan error, 1)
	go func() {
		turn, err := client.StartTurn(context.Background(), map[string]string{"threadId": "thread-1"})
		if err == nil && turn.ID != "turn-1" {
			err = errors.New("wrong turn ID")
		}
		turnDone <- err
	}()
	request = server.receive(t)
	server.send(t, map[string]any{"id": json.RawMessage(request.ID), "result": map[string]any{"turn": map[string]string{"id": "turn-1"}}})
	if err := <-turnDone; err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- client.Shutdown(context.Background()) }()
	unsubscribe := server.receive(t)
	if unsubscribe.Method != "thread/unsubscribe" || !strings.Contains(string(unsubscribe.Params), "thread-1") {
		t.Fatalf("shutdown message = %#v", unsubscribe)
	}
	server.send(t, map[string]any{"id": json.RawMessage(unsubscribe.ID), "result": map[string]string{"status": "unsubscribed"}})
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAppServerClientFailsPendingCallOnMalformedFrame(t *testing.T) {
	t.Parallel()

	client, server := newTestClient(t, "0.151.0-alpha.7.2")
	completed := make(chan error, 1)
	go func() {
		var result map[string]any
		completed <- client.Call(context.Background(), "test/read", struct{}{}, &result)
	}()
	_ = server.receive(t)
	server.mu.Lock()
	_, _ = server.replies.Write([]byte("{bad json}\n"))
	server.mu.Unlock()
	select {
	case err := <-completed:
		if err == nil || !strings.Contains(err.Error(), "decode Codex App Server message") {
			t.Fatalf("Call() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call did not fail after malformed frame")
	}
}
