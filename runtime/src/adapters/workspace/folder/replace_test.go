package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	port "darkstar/src/ports/workspace"
)

func digest(data string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
}

func TestCompareAndSwapReplacementPreservesPriorBytesOnFailure(t *testing.T) {
	ctx := context.Background()
	f, err := New(t.TempDir(), Limits{FileBytes: 8, AreaBytes: 10, AreaFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := f.Bind(ctx, port.Grant{WorkItemID: "work", PluginID: "plugin", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.WriteFile(ctx, port.PluginState, "nested/state", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteFile(ctx, port.PluginState, "other", []byte("extra")); err != nil {
		t.Fatal(err)
	}
	// Replacing 3 bytes with 5 consumes 10 total, not old+new+other (13).
	if err := h.ReplaceFile(ctx, port.PluginState, "nested/state", digest("old"), []byte("newer")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, expected, data string }{
		{"stale", digest("old"), "bad"}, {"missing digest", "", "bad"}, {"over quota", digest("newer"), "123456"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := h.ReplaceFile(ctx, port.PluginState, "nested/state", tc.expected, []byte(tc.data)); err == nil {
				t.Fatal("invalid replacement succeeded")
			}
			data, err := h.ReadFile(ctx, port.PluginState, "nested/state")
			if err != nil || string(data) != "newer" {
				t.Fatalf("prior data changed: %q, %v", data, err)
			}
		})
	}
	if err := h.ReplaceFile(ctx, port.PluginState, "missing", digest(""), nil); err == nil {
		t.Fatal("CAS created absent file")
	}
	if err := h.ReplaceFile(ctx, port.PluginState, "nested/state", digest("newer"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := h.ReplaceFile(ctx, port.PluginState, "other", digest("extra"), []byte("12345678")); err != nil {
		t.Fatalf("shrink did not release quota: %v", err)
	}
}

func TestConcurrentCompareAndSwapHasOneWinner(t *testing.T) {
	ctx := context.Background()
	f, err := New(t.TempDir(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	grant := port.Grant{WorkItemID: "work", PluginID: "plugin", AttemptID: "attempt"}
	h, err := f.Bind(ctx, grant)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.WriteFile(ctx, port.Staged, "draft.md", []byte("original")); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			other, err := f.Bind(ctx, grant)
			if err != nil {
				results <- err
				return
			}
			defer other.Close()
			results <- other.ReplaceFile(ctx, port.Staged, "draft.md", digest("original"), []byte(fmt.Sprintf("revision-%d", i)))
		}(i)
	}
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d", winners)
	}
}

func TestReplacementRejectsLinkedTargetAndParent(t *testing.T) {
	ctx := context.Background()
	f, err := New(t.TempDir(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := f.Bind(ctx, port.Grant{WorkItemID: "work", PluginID: "plugin", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	root := h.(*handle).roots[port.Staged]
	if err := os.Symlink(outside, filepath.Join(root.Name(), "escape")); err != nil {
		t.Skipf("symlink permission unavailable: %v", err)
	}
	if err := h.ReplaceFile(ctx, port.Staged, "escape/secret", digest("secret"), []byte("changed")); err == nil {
		t.Fatal("replaced outside root")
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root.Name(), "alias")); err != nil {
		t.Fatal(err)
	}
	if err := h.ReplaceFile(ctx, port.Staged, "alias", digest("secret"), []byte("changed")); err == nil {
		t.Fatal("replaced symbolic target")
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(data) != "secret" {
		t.Fatalf("external bytes changed %q: %v", data, err)
	}
}
