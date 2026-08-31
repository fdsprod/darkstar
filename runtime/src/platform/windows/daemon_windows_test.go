package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/daemon"
)

func TestDaemonLockIsExclusiveForHandleLifetime(t *testing.T) {
	t.Parallel()

	host := NewDaemonHost()
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := host.AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if _, err := host.AcquireLock(path); !errors.Is(err, daemon.ErrLockHeld) {
		t.Fatalf("second AcquireLock() error = %v, want ErrLockHeld", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := host.AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock() after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStopEventSignalsExactInstance(t *testing.T) {
	t.Parallel()

	host := NewDaemonHost()
	digest := sha256.Sum256([]byte(t.Name()))
	instanceID := hex.EncodeToString(digest[:16])
	event, err := host.CreateStopEvent(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer event.Close()
	if err := host.SignalStop(instanceID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := event.Wait(ctx); err != nil {
		t.Fatalf("Wait() after SignalStop(): %v", err)
	}
}

func TestInspectProcessRequiresCreationTimeAndExecutableIdentity(t *testing.T) {
	t.Parallel()

	host := NewDaemonHost()
	identity, err := host.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := host.InspectProcess(identity)
	if err != nil {
		t.Fatal(err)
	}
	if inspection != daemon.ProcessIdentityMatches {
		t.Fatalf("InspectProcess(current) = %v, want match", inspection)
	}

	identity.StartedAt = identity.StartedAt.Add(100 * time.Nanosecond)
	inspection, err = host.InspectProcess(identity)
	if err != nil {
		t.Fatal(err)
	}
	if inspection != daemon.ProcessIdentityDiffers {
		t.Fatalf("InspectProcess(reused PID identity) = %v, want differs", inspection)
	}
}
