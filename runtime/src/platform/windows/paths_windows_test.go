package windows

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"darkstar/src/ports/platform"
)

func TestResolvePathsUsesLocalAppDataContract(t *testing.T) {
	t.Parallel()

	localAppData := filepath.Join(`C:\Users`, "configuration tester", "AppData", "Local")
	resolver := &PathResolver{localAppData: func() (string, error) { return localAppData, nil }}
	got, err := resolver.ResolvePaths(context.Background(), platform.PathRequest{ApplicationName: "DARKSTAR"})
	if err != nil {
		t.Fatalf("ResolvePaths() error = %v", err)
	}
	root := filepath.Join(localAppData, "DARKSTAR")
	want := platform.Paths{
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Cache:   filepath.Join(root, "cache"),
		Logs:    filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolvePaths() = %#v, want %#v", got, want)
	}
	for name, path := range map[string]string{
		"config": got.Config, "data": got.Data, "cache": got.Cache,
		"logs": got.Logs, "runtime": got.Runtime,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s path %q is not absolute", name, path)
		}
	}
}

func TestResolvePathsRejectsUnsafeApplicationNames(t *testing.T) {
	t.Parallel()

	resolver := &PathResolver{localAppData: func() (string, error) { return `C:\Users\tester\AppData\Local`, nil }}
	for _, name := range []string{"", "   ", " DARKSTAR", "DARKSTAR ", "DARKSTAR.", "DARK:STAR", "DARK*STAR", "DARK\nSTAR", "NUL", "con.txt", "COM1", ".", "..", `DARKSTAR\..\escape`, `C:\DARKSTAR`, "nested/app"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := resolver.ResolvePaths(context.Background(), platform.PathRequest{ApplicationName: name}); err == nil {
				t.Fatalf("ResolvePaths(%q) error = nil, want validation error", name)
			}
		})
	}
}

func TestResolvePathsPropagatesContextAndKnownFolderFailures(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolver := &PathResolver{localAppData: func() (string, error) { return "", errors.New("must not run") }}
	if _, err := resolver.ResolvePaths(ctx, platform.PathRequest{ApplicationName: "DARKSTAR"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolvePaths(canceled) error = %v, want context.Canceled", err)
	}

	want := errors.New("known folder unavailable")
	resolver = &PathResolver{localAppData: func() (string, error) { return "", want }}
	if _, err := resolver.ResolvePaths(context.Background(), platform.PathRequest{ApplicationName: "DARKSTAR"}); !errors.Is(err, want) {
		t.Fatalf("ResolvePaths(failure) error = %v, want wrapped %v", err, want)
	}
}
