// Package toolchain resolves project-pinned tools independently of the host PATH.
package toolchain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"darkstar/src/platform/process"
)

type binding struct {
	SchemaVersion int    `json:"schemaVersion"`
	GoRoot        string `json:"goRoot"`
	NodeRoot      string `json:"nodeRoot"`
}

type versionCheck struct {
	name, executable string
	args             []string
	expected         string
}

// Activate configures only this daemon process, including executable lookup for
// daemon-owned checks. It never changes the user's or system's saved PATH.
func Activate(environment []string) error {
	for _, key := range []string{"PATH", "GOROOT", "GOTOOLCHAIN", "DARKSTAR_TOOLCHAIN_BINDING", "GOCACHE", "GOMODCACHE", "npm_config_cache"} {
		if v := value(environment, key); v != "" {
			if err := os.Setenv(key, v); err != nil {
				return err
			}
		}
	}
	return nil
}

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Environment returns an explicit process environment. No tools are downloaded,
// global settings changed, or shell scripts run during admission.
func Environment(ctx context.Context, root string, inherited []string) ([]string, error) {
	return resolve(ctx, root, inherited, probe)
}

func resolve(ctx context.Context, root string, inherited []string, run func(context.Context, string, []string, []string) (string, error)) ([]string, error) {
	pins := map[string]string{}
	for _, name := range []string{"go", "node", "npm"} {
		raw, err := os.ReadFile(filepath.Join(root, "."+name+"-version"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		value := strings.TrimSpace(string(raw))
		if !versionPattern.MatchString(value) {
			return nil, fmt.Errorf("invalid .%s-version pin", name)
		}
		pins[name] = value
	}
	if len(pins) == 0 {
		return append([]string(nil), inherited...), nil
	}
	path := filepath.Join(root, ".darkstar", "toolchains.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("project toolchains are not configured; run ./scripts/Setup-Toolchain.ps1 in %s: %w", root, err)
	}
	var tools binding
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&tools); err != nil || tools.SchemaVersion != 1 {
		return nil, errors.New("invalid .darkstar/toolchains.json binding; rerun ./scripts/Setup-Toolchain.ps1")
	}
	environment := append([]string(nil), inherited...)
	var directories []string
	if pins["go"] != "" {
		if !filepath.IsAbs(tools.GoRoot) {
			return nil, errors.New("toolchain Go root must be absolute")
		}
		directories = append(directories, filepath.Join(tools.GoRoot, "bin"))
		environment = replace(environment, "GOROOT", tools.GoRoot)
		environment = replace(environment, "GOTOOLCHAIN", "local")
	}
	if pins["node"] != "" || pins["npm"] != "" {
		if !filepath.IsAbs(tools.NodeRoot) {
			return nil, errors.New("toolchain Node root must be absolute")
		}
		directories = append(directories, tools.NodeRoot)
	}
	if inheritedPath := value(environment, "PATH"); inheritedPath != "" {
		directories = append(directories, inheritedPath)
	}
	environment = replace(environment, "PATH", strings.Join(directories, string(os.PathListSeparator)))
	environment = replace(environment, "DARKSTAR_TOOLCHAIN_BINDING", path)
	for key, directory := range map[string]string{"GOCACHE": "go-build", "GOMODCACHE": "go-mod", "npm_config_cache": "npm"} {
		cache := filepath.Join(root, ".darkstar", "cache", directory)
		if err := os.MkdirAll(cache, 0700); err != nil {
			return nil, fmt.Errorf("prepare %s: %w", key, err)
		}
		environment = replace(environment, key, cache)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	checks := []versionCheck{}
	if pin := pins["go"]; pin != "" {
		checks = append(checks, versionCheck{"Go", filepath.Join(tools.GoRoot, "bin", "go"+suffix), []string{"version"}, pin})
	}
	if pin := pins["node"]; pin != "" {
		checks = append(checks, versionCheck{"Node", filepath.Join(tools.NodeRoot, "node"+suffix), []string{"--version"}, pin})
	}
	if pin := pins["npm"]; pin != "" {
		for _, name := range []string{"npm", "npx"} {
			cli := filepath.Join(tools.NodeRoot, "node_modules", "npm", "bin", name+"-cli.js")
			if _, err = os.Stat(cli); err != nil {
				return nil, fmt.Errorf("%s launcher is missing; rerun ./scripts/Setup-Toolchain.ps1: %w", name, err)
			}
			checks = append(checks, versionCheck{name, filepath.Join(tools.NodeRoot, "node"+suffix), []string{cli, "--version"}, pin})
		}
	}
	for _, check := range checks {
		output, err := run(ctx, check.executable, check.args, environment)
		matches := meetsMinimum(check.name, output, check.expected)
		if err != nil || !matches {
			return nil, fmt.Errorf("project %s toolchain check failed (expected >= %s, got %q): %v; rerun ./scripts/Setup-Toolchain.ps1", check.name, check.expected, strings.TrimSpace(output), err)
		}
	}
	return environment, nil
}

func meetsMinimum(name, output, minimum string) bool {
	actual := strings.TrimSpace(output)
	if name == "Go" {
		fields := strings.Fields(actual)
		if len(fields) != 4 || fields[0] != "go" || fields[1] != "version" || !strings.HasPrefix(fields[2], "go") {
			return false
		}
		actual = strings.TrimPrefix(fields[2], "go")
	} else if name == "Node" {
		if !strings.HasPrefix(actual, "v") {
			return false
		}
		actual = strings.TrimPrefix(actual, "v")
	}
	return versionPattern.MatchString(actual) && version.Compare("go"+actual, "go"+minimum) >= 0
}

func probe(ctx context.Context, executable string, args, environment []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = environment
	process.HideConsole(cmd)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
func value(environment []string, key string) string {
	for _, entry := range environment {
		name, v, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) {
			return v
		}
	}
	return ""
}
func replace(environment []string, key, newValue string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, key) {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+newValue)
}
