package toolchain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, binding) {
	t.Helper()
	root := t.TempDir()
	b := binding{SchemaVersion: 1, GoRoot: filepath.Join(root, "tools", "go"), NodeRoot: filepath.Join(root, "tools", "node")}
	for name, content := range map[string]string{".go-version": "1.24.0", ".node-version": "22.12.0", ".npm-version": "10.9.0", "tools/node/node_modules/npm/bin/npm-cli.js": "", "tools/node/node_modules/npm/bin/npx-cli.js": ""} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".darkstar"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(filepath.Join(root, ".darkstar", "toolchains.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return root, b
}

func TestPinnedToolsOverrideHostVersionsAndReachChildEnvironment(t *testing.T) {
	root, b := fixture(t)
	calls := 0
	inherited := []string{"Path=host-tools", "PATH=another-stale-path", "GOROOT=host-go", "GOTOOLCHAIN=auto", "KEEP=present"}
	result, err := resolve(t.Context(), root, inherited, func(_ context.Context, executable string, args, env []string) (string, error) {
		calls++
		if !strings.HasPrefix(executable, filepath.Join(root, "tools")) {
			t.Fatalf("host executable selected: %s", executable)
		}
		if value(env, "GOROOT") != b.GoRoot || value(env, "GOTOOLCHAIN") != "local" || !strings.HasPrefix(value(env, "PATH"), filepath.Join(b.GoRoot, "bin")) {
			t.Fatalf("wrong child environment")
		}
		if args[0] == "version" {
			return "go version go1.24.0 windows/amd64", nil
		}
		if args[0] == "--version" {
			return "v22.12.0", nil
		}
		return "10.9.0", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 || value(result, "KEEP") != "present" || value(result, "DARKSTAR_TOOLCHAIN_BINDING") != filepath.Join(root, ".darkstar", "toolchains.json") {
		t.Fatalf("incomplete environment/probes: %d", calls)
	}
	count := 0
	for _, entry := range result {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "PATH") {
			count++
		}
	}
	if count != 1 || inherited[0] != "Path=host-tools" {
		t.Fatal("duplicate PATH or mutated inherited environment")
	}
}

func TestToolchainAdmissionRejectsOlderVersionOrBrokenNpx(t *testing.T) {
	root, b := fixture(t)
	_, err := resolve(t.Context(), root, nil, func(context.Context, string, []string, []string) (string, error) {
		return "go version go1.23.9 windows/amd64", nil
	})
	if err == nil || !strings.Contains(err.Error(), "toolchain check failed") {
		t.Fatalf("older Go accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(b.NodeRoot, "node_modules", "npm", "bin", "npx-cli.js")); err != nil {
		t.Fatal(err)
	}
	_, err = resolve(t.Context(), root, nil, func(context.Context, string, []string, []string) (string, error) {
		t.Fatal("probed incomplete installation")
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "npx launcher is missing") {
		t.Fatalf("broken npx accepted: %v", err)
	}
}

func TestMinimumVersions(t *testing.T) {
	for _, tc := range []struct {
		name, output, minimum string
		want                  bool
	}{
		{"Go", "go version go1.24.0 windows/amd64", "1.24.0", true},
		{"Go", "go version go1.27.0 windows/amd64", "1.24.0", true},
		{"Go", "go version go1.23.12 windows/amd64", "1.24.0", false},
		{"Go", "go version go1.28rc1 windows/amd64", "1.24.0", false},
		{"Node", "v24.15.0", "22.12.0", true},
		{"Node", "v22.9.0", "22.12.0", false},
		{"npm", "10.10.0", "10.9.0", true},
		{"npx", "11.0.0", "10.9.0", true},
		{"npm", "broken", "10.9.0", false},
	} {
		t.Run(tc.name+tc.output, func(t *testing.T) {
			if got := meetsMinimum(tc.name, tc.output, tc.minimum); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnpinnedProjectsKeepEnvironmentAndMissingBindingsFailEarly(t *testing.T) {
	root := t.TempDir()
	got, err := Environment(t.Context(), root, []string{"PATH=existing"})
	if err != nil || len(got) != 1 || got[0] != "PATH=existing" {
		t.Fatalf("unconfigured project changed: %v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".go-version"), []byte("1.24.0"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = Environment(t.Context(), root, nil)
	if err == nil || !strings.Contains(err.Error(), "Setup-Toolchain.ps1") {
		t.Fatalf("missing setup lacked remedy: %v", err)
	}
}
