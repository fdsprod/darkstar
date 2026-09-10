// Package pluginprocess hosts explicitly registered single-file JavaScript plugins.
// A child process is a fault boundary, not a filesystem security sandbox.
package pluginprocess

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
	"time"

	"darkstar/src/adapters/valueschema/jsonschema"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/plugin"
)

const maxMessage = 1024 * 1024

type Config struct {
	Executable          string
	Entrypoint          string
	Ref                 extension.Ref
	GrantedCapabilities []string
	Timeout             time.Duration
}
type Host struct{ config Config }

func New(config Config) (*Host, error) {
	if err := config.Ref.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(config.Entrypoint) || !filepath.IsAbs(config.Executable) {
		return nil, errors.New("plugin executable and entrypoint must be absolute host-configured paths")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	config.GrantedCapabilities = append([]string(nil), config.GrantedCapabilities...)
	host := &Host{config: config}
	if err := host.verify(); err != nil {
		return nil, err
	}
	return host, nil
}
func (h *Host) verify() error {
	data, err := os.ReadFile(h.config.Entrypoint)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != h.config.Ref.Digest {
		return errors.New("PLUGIN_DIGEST_MISMATCH: entrypoint changed")
	}
	return nil
}
func (h *Host) Describe(ctx context.Context) (plugin.Descriptor, error) {
	raw, err := h.exchange(ctx, "describe", json.RawMessage(`{}`), nil, nil)
	if err != nil {
		return plugin.Descriptor{}, err
	}
	var d plugin.Descriptor
	descriptorDecoder := json.NewDecoder(bytes.NewReader(raw))
	descriptorDecoder.DisallowUnknownFields()
	if err = descriptorDecoder.Decode(&d); err != nil {
		return d, err
	}
	if d.Protocol != plugin.Protocol || d.Ref.ID != h.config.Ref.ID || d.Ref.Version != h.config.Ref.Version {
		return d, errors.New("PLUGIN_INCOMPATIBLE: descriptor identity or protocol differs")
	}
	d.Ref = h.config.Ref
	seenKinds, seenIDs := map[string]bool{}, map[string]bool{}
	for _, r := range d.Resources {
		if r.Kind == "" || r.Tool.ID == "" || seenKinds[r.Kind] || seenIDs[r.Tool.ID] {
			return d, errors.New("PLUGIN_INVALID_DESCRIPTOR: duplicate or empty resource/tool")
		}
		seenKinds[r.Kind], seenIDs[r.Tool.ID] = true, true
		if r.CreateOperation == "" || r.CreateOperation == "read" {
			return d, errors.New("PLUGIN_INVALID_DESCRIPTOR: invalid create operation")
		}
		operations := map[string]bool{"read": true, r.CreateOperation: true}
		for _, operation := range r.UpdateOperations {
			if operation == "" || operations[operation] {
				return d, errors.New("PLUGIN_INVALID_DESCRIPTOR: duplicate or empty operation")
			}
			operations[operation] = true
		}
	}
	tools := append([]plugin.Tool(nil), d.Tools...)
	for _, r := range d.Resources {
		tools = append(tools, r.Tool)
	}
	seenIDs = map[string]bool{}
	for _, tool := range tools {
		if tool.ID == "" || seenIDs[tool.ID] {
			return d, errors.New("PLUGIN_INVALID_DESCRIPTOR: duplicate or empty tool")
		}
		seenIDs[tool.ID] = true
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.ResultSchema} {
			if len(schema) == 0 {
				return d, errors.New("PLUGIN_INVALID_DESCRIPTOR: missing tool schema")
			}
			if err := (jsonschema.Validator{}).Validate(schema, nil); err != nil {
				return d, fmt.Errorf("PLUGIN_INVALID_DESCRIPTOR: %w", err)
			}
		}
		for _, capability := range tool.RequiredCapabilities {
			if !contains(h.config.GrantedCapabilities, capability) {
				return d, fmt.Errorf("PLUGIN_CAPABILITY_DENIED: %s", capability)
			}
		}
	}
	return d, nil
}
func (h *Host) Invoke(ctx context.Context, invocation plugin.Invocation, services plugin.HostServices) (json.RawMessage, error) {
	d, err := h.Describe(ctx)
	if err != nil {
		return nil, err
	}
	var tool *plugin.Tool
	for _, r := range d.Resources {
		if r.Tool.ID == invocation.Contribution {
			copy := r.Tool
			tool = &copy
			break
		}
	}
	for _, candidate := range d.Tools {
		if candidate.ID == invocation.Contribution {
			copy := candidate
			tool = &copy
			break
		}
	}
	if tool == nil {
		return nil, errors.New("PLUGIN_UNKNOWN_CONTRIBUTION")
	}
	if len(invocation.Arguments) == 0 || len(invocation.Arguments) > maxMessage {
		return nil, errors.New("PLUGIN_INVALID_ARGUMENTS: empty or oversized")
	}
	if err := (jsonschema.Validator{}).Validate(tool.InputSchema, invocation.Arguments); err != nil {
		return nil, fmt.Errorf("PLUGIN_INVALID_ARGUMENTS: %w", err)
	}
	raw, _ := json.Marshal(invocation)
	result, err := h.exchange(ctx, "invoke", raw, services, tool.RequiredCapabilities)
	if err != nil {
		return nil, err
	}
	if err := (jsonschema.Validator{}).Validate(tool.ResultSchema, result); err != nil {
		return nil, fmt.Errorf("PLUGIN_INVALID_RESULT: %w", err)
	}
	return result, nil
}

type envelope struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

type boundedLog struct{ data []byte }

func (b *boundedLog) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data) < 8192 {
		remaining := 8192 - len(b.data)
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (h *Host) exchange(parent context.Context, method string, params json.RawMessage, services plugin.HostServices, capabilities []string) (json.RawMessage, error) {
	if err := h.verify(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, h.config.Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, h.config.Executable, h.config.Entrypoint)
	hideWindow(command)
	command.Dir = filepath.Dir(h.config.Entrypoint)
	// No daemon secrets are forwarded through the environment. The trusted Node
	// executable still has OS access; permission isolation is a separate deployment concern.
	command.Env = []string{}
	if value := os.Getenv("SystemRoot"); value != "" {
		command.Env = append(command.Env, "SystemRoot="+value)
	}
	command.WaitDelay = time.Second
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	logs := &boundedLog{}
	command.Stderr = logs
	if err = command.Start(); err != nil {
		return nil, err
	}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			// Also unblock protocol pipes if a descendant inherited them.
			_ = input.Close()
			_ = output.Close()
		case <-finished:
		}
	}()
	defer func() {
		input.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()
	send := func(message envelope) error {
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if len(data) > maxMessage {
			return errors.New("PLUGIN_MESSAGE_TOO_LARGE")
		}
		_, err = input.Write(append(data, '\n'))
		return err
	}
	if err = send(envelope{Type: "request", ID: "1", Method: method, Params: params}); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), maxMessage+1)
	callbacks := map[string]bool{}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var message envelope
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&message); err != nil {
			return nil, fmt.Errorf("PLUGIN_INVALID_MESSAGE: %w", err)
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, errors.New("PLUGIN_INVALID_MESSAGE: trailing JSON")
		}
		switch message.Type {
		case "response":
			if message.ID != "1" || message.Method != "" || len(message.Params) != 0 {
				return nil, errors.New("PLUGIN_INVALID_RESPONSE")
			}
			if message.Error != "" {
				if len(message.Result) != 0 {
					return nil, errors.New("PLUGIN_INVALID_RESPONSE: result and error are mutually exclusive")
				}
				return nil, fmt.Errorf("PLUGIN_ERROR: %s", message.Error)
			}
			if len(message.Result) == 0 {
				return nil, errors.New("PLUGIN_INVALID_RESPONSE: missing result")
			}
			return message.Result, nil
		case "host_call":
			if message.Error != "" || len(message.Result) != 0 || message.Method == "" || len(message.Params) == 0 {
				return nil, errors.New("PLUGIN_INVALID_HOST_CALL: unexpected fields")
			}
			if message.ID == "" || callbacks[message.ID] || len(callbacks) >= 128 {
				return nil, errors.New("PLUGIN_INVALID_HOST_CALL")
			}
			callbacks[message.ID] = true
			if services == nil || !contains(capabilities, message.Method) || !contains(h.config.GrantedCapabilities, message.Method) {
				return nil, fmt.Errorf("PLUGIN_CAPABILITY_DENIED: %s", message.Method)
			}
			result, callErr := services.Call(ctx, message.Method, message.Params)
			response := envelope{Type: "host_result", ID: message.ID, Result: result}
			if callErr != nil {
				response.Result = nil
				response.Error = callErr.Error()
			}
			if err = send(response); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("PLUGIN_INVALID_MESSAGE: unknown message type")
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("PLUGIN_INVALID_MESSAGE: %w", scanner.Err())
	}
	return nil, errors.New("PLUGIN_PROCESS_EXITED: response missing")
}
