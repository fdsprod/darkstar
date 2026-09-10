package typescript

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestBlockedProviderWriteHonorsDeadline(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &session{input: writer, output: reader, writes: make(chan struct{}, 1), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	deadline, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stop()
	if err := s.send(deadline, message{Type: "request", ID: "1", Method: "provider.events"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write returned %v", err)
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("partially written transport was not closed")
	}
}

func TestWaitingForWriteSlotDoesNotCancelProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	s := &session{writes: make(chan struct{}, 1), done: make(chan struct{})}
	s.writes <- struct{}{}
	if err := s.send(ctx, message{Type: "request", ID: "1"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write slot returned %v", err)
	}
	select {
	case <-s.done:
		t.Fatal("unsent cancelled request closed an otherwise healthy transport")
	default:
	}
}
