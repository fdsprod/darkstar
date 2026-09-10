package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	port "darkstar/src/ports/workspace"
)

func TestIsolationPersistenceAndExclusiveVersions(t *testing.T) {
	ctx := context.Background()
	f, err := New(t.TempDir(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bind := func(work, plugin, attempt string) port.Handle {
		h, err := f.Bind(ctx, port.Grant{WorkItemID: work, PluginID: plugin, AttemptID: attempt})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			h.Close()
		})
		return h
	}
	a := bind("work-a", "builtin/items", "attempt-a")
	for _, area := range []port.Area{port.PluginState, port.Scratch, port.Staged} {
		if err := a.WriteFile(ctx, area, "nested/version-1.json", []byte("one")); err != nil {
			t.Fatal(err)
		}
		if err := a.WriteFile(ctx, area, "nested/version-1.json", []byte("two")); !errors.Is(err, os.ErrExist) {
			t.Fatalf("overwrite = %v", err)
		}
	}
	for _, other := range []port.Handle{bind("work-b", "builtin/items", "attempt-a"), bind("work-a", "builtin/decisions", "attempt-a")} {
		if _, err := other.ReadFile(ctx, port.PluginState, "nested/version-1.json"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cross-owner read = %v", err)
		}
	}
	retry := bind("work-a", "builtin/items", "attempt-b")
	data, err := retry.ReadFile(ctx, port.PluginState, "nested/version-1.json")
	if err != nil || string(data) != "one" {
		t.Fatalf("persistent state %q: %v", data, err)
	}
	if _, err := retry.ReadFile(ctx, port.Staged, "nested/version-1.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt isolation = %v", err)
	}
	a.Close()
	if _, err := a.ReadFile(ctx, port.Staged, "nested/version-1.json"); err == nil {
		t.Fatal("closed handle remained usable")
	}
	reopened := bind("work-a", "builtin/items", "attempt-a")
	if _, err := reopened.ReadFile(ctx, port.Staged, "nested/version-1.json"); err != nil {
		t.Fatal(err)
	}
}

func TestContainmentAndLimits(t *testing.T) {
	ctx := context.Background()
	f, err := New(t.TempDir(), Limits{FileBytes: 4, AreaBytes: 6, AreaFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := f.Bind(ctx, port.Grant{WorkItemID: "work", PluginID: "plugin", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, name := range []string{"../escape", "/escape", `C:\escape`, `..\escape`, "folder/../escape", "x:stream", "folder./file", "."} {
		if err := h.WriteFile(ctx, port.Staged, name, nil); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if err := h.WriteFile(ctx, port.Area("unknown"), "one", nil); err == nil {
		t.Fatal("accepted unknown area")
	}
	if err := h.WriteFile(ctx, port.Staged, "big", []byte("12345")); err == nil {
		t.Fatal("accepted oversize")
	}
	if err := h.WriteFile(ctx, port.Staged, "one", []byte("1234")); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteFile(ctx, port.Staged, "two", []byte("123")); err == nil {
		t.Fatal("accepted over quota")
	}
	if err := h.WriteFile(ctx, port.Staged, "two", []byte("12")); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteFile(ctx, port.Staged, "three", nil); err == nil {
		t.Fatal("accepted excess file count")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.WriteFile(cancelled, port.Scratch, "cancelled", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestRejectSymlinkEscapeAndNamespaceAliases(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	f, err := New(directory, Limits{})
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
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if _, err := h.ReadFile(ctx, port.Staged, "escape/secret"); err == nil {
		t.Fatal("read escaped workspace")
	}
	if err := h.WriteFile(ctx, port.Staged, "escape/changed", nil); err == nil {
		t.Fatal("write escaped workspace")
	}
}
