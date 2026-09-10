// Package typescript adapts the versioned provider plugin protocol to the
// application-owned provider port. It contains no provider-native RPC mapping.
package typescript

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"darkstar/src/platform/process"
)

const messageLimit = 16 << 20

type message struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}
type response struct {
	result json.RawMessage
	err    error
}
type session struct {
	input     io.WriteCloser
	output    io.ReadCloser
	owner     *process.Owner
	mu        sync.Mutex
	writes    chan struct{}
	next      uint64
	pending   map[string]chan response
	done      chan struct{}
	err       error
	once      sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
	callback  func(context.Context, string, json.RawMessage) (json.RawMessage, error)
	callbacks chan struct{}
}

func startSession(config Config, callback func(context.Context, string, json.RawMessage) (json.RawMessage, error)) (*session, error) {
	if err := config.verify(); err != nil {
		return nil, err
	}
	command := exec.Command(config.NodeExecutable, config.Entrypoint)
	command.Dir = filepath.Dir(config.Entrypoint)
	command.Env = []string{}
	if root := os.Getenv("SystemRoot"); root != "" {
		command.Env = append(command.Env, "SystemRoot="+root)
	}
	process.PrepareOwned(command)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	// Provider-native raw evidence is separately persisted through a scoped
	// callback. Diagnostic stderr cannot fill an unbounded memory buffer.
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	owner, err := process.Own(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{input: input, output: output, owner: owner, writes: make(chan struct{}, 1), pending: map[string]chan response{}, done: make(chan struct{}), ctx: ctx, cancel: cancel, callback: callback, callbacks: make(chan struct{}, 32)}
	go s.read()
	return s, nil
}
func (s *session) fail(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		s.cancel()
		close(s.done)
		_ = s.input.Close()
		_ = s.output.Close()
		if s.owner != nil {
			_ = s.owner.Kill()
		}
	})
}
func (s *session) send(ctx context.Context, m message) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(raw) > messageLimit {
		return errors.New("provider plugin message exceeds limit")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("provider plugin is closed")
	case s.writes <- struct{}{}:
	}
	written := make(chan error, 1)
	go func() {
		defer func() {
			<-s.writes
		}()
		_, writeErr := s.input.Write(append(raw, '\n'))
		written <- writeErr
	}()
	select {
	case err := <-written:
		if err != nil {
			s.fail(err)
		}
		return err
	case <-ctx.Done():
		// A partially written JSON frame cannot be retried on this transport.
		s.fail(ctx.Err())
		return ctx.Err()
	case <-s.done:
		return errors.New("provider plugin is closed")
	}
}
func (s *session) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if len(s.pending) >= 128 {
		s.mu.Unlock()
		return errors.New("too many outstanding provider requests")
	}
	s.next++
	id := strconv.FormatUint(s.next, 10)
	ch := make(chan response, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	if err = s.send(ctx, message{Type: "request", ID: id, Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		s.mu.Lock()
		err = s.err
		s.mu.Unlock()
		return err
	case reply := <-ch:
		if reply.err != nil {
			return reply.err
		}
		if out == nil {
			return nil
		}
		d := json.NewDecoder(bytes.NewReader(reply.result))
		d.DisallowUnknownFields()
		if err = d.Decode(out); err != nil {
			return fmt.Errorf("invalid provider response for %s: %w", method, err)
		}
		if d.Decode(new(any)) != io.EOF {
			return errors.New("provider response contains trailing JSON")
		}
		return nil
	}
}
func (s *session) read() {
	scanner := bufio.NewScanner(s.output)
	scanner.Buffer(make([]byte, 4096), messageLimit+1)
	for scanner.Scan() {
		var m message
		d := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		d.DisallowUnknownFields()
		if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.ID == "" {
			s.fail(errors.New("invalid provider plugin envelope"))
			return
		}
		switch m.Type {
		case "response":
			if m.Method != "" || len(m.Params) != 0 || (m.Error != "" && len(m.Result) != 0) || (m.Error == "" && len(m.Result) == 0) {
				s.fail(errors.New("invalid provider response"))
				return
			}
			s.mu.Lock()
			ch := s.pending[m.ID]
			s.mu.Unlock()
			if ch == nil {
				continue
			} // A cancelled observation can finish later.
			r := response{result: m.Result}
			if m.Error != "" {
				r.err = errors.New(m.Error)
			}
			select {
			case ch <- r:
			default:
				s.fail(errors.New("duplicate provider response"))
				return
			}
		case "host_call":
			if m.Method == "" || len(m.Params) == 0 || len(m.Result) != 0 || m.Error != "" {
				s.fail(errors.New("invalid provider host callback"))
				return
			}
			select {
			case s.callbacks <- struct{}{}:
			default:
				s.fail(errors.New("provider callback limit exceeded"))
				return
			}
			go func() {
				defer func() {
					<-s.callbacks
				}()
				ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
				defer cancel()
				raw, err := s.callback(ctx, m.Method, m.Params)
				reply := message{Type: "host_result", ID: m.ID, Result: raw}
				if err != nil {
					reply.Result = nil
					reply.Error = err.Error()
				}
				if err = s.send(ctx, reply); err != nil {
					s.fail(err)
				}
			}()
		default:
			s.fail(errors.New("unknown provider plugin message"))
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("provider plugin exited")
	}
	s.fail(err)
}
func (s *session) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.call(ctx, "provider.shutdown", map[string]any{}, nil)
	s.fail(errors.New("provider plugin closed"))
	return nil
}
