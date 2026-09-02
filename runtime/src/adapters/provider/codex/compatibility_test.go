package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fixtureManifest struct {
	FixtureSchemaVersion int    `json:"fixtureSchemaVersion"`
	Scenario             string `json:"scenario"`
	Status               string `json:"status"`
	Platform             string `json:"platform"`
	CodexVersion         string `json:"codexVersion"`
	Transport            string `json:"transport"`
	FailureClass         string `json:"failureClass"`
	Fixture              string `json:"fixture"`
}

func TestSupportedCodexVersionsHaveRequiredCompatibilityFixtures(t *testing.T) {
	manifests := loadFixtureManifests(t)
	passed := make(map[string]map[string]map[string]bool)
	expectedFailures := make(map[string]map[string]bool)
	for _, manifest := range manifests {
		if passed[manifest.Transport] == nil {
			passed[manifest.Transport] = make(map[string]map[string]bool)
		}
		if passed[manifest.Transport][manifest.CodexVersion] == nil {
			passed[manifest.Transport][manifest.CodexVersion] = make(map[string]bool)
		}
		if manifest.Status == "passed" {
			passed[manifest.Transport][manifest.CodexVersion][manifest.Scenario] = true
		}
		if manifest.Status == "failed-as-expected" {
			if expectedFailures[manifest.CodexVersion] == nil {
				expectedFailures[manifest.CodexVersion] = make(map[string]bool)
			}
			expectedFailures[manifest.CodexVersion][manifest.FailureClass] = true
		}
	}

	appServerRequirements := map[string][]string{
		"0.151.0-alpha.7.1": {"handshake", "read-only"},
		"0.151.0-alpha.7.2": {"read-only", "resume", "write-approval", "interrupt", "process-kill", "image-skill", "user-input"},
	}
	for _, version := range supportedAppServerVersions {
		required, declared := appServerRequirements[version]
		if !declared {
			t.Errorf("supported App Server version %s has no explicit fixture requirements", version)
			continue
		}
		assertScenarios(t, "stdio-jsonrpc-jsonl", version, required, passed)
	}

	for _, version := range defaultSupportedExecVersions {
		assertScenarios(t, "exec-jsonl", version, []string{"read-only", "resume", "process-kill"}, passed)
		if !expectedFailures[version]["cli-compatibility-drift"] {
			t.Errorf("supported exec version %s has no reviewed CLI flag-drift fixture", version)
		}
	}
}

func loadFixtureManifests(t *testing.T) []fixtureManifest {
	t.Helper()
	root := codexFixtureRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.manifest.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("find compatibility manifests: paths=%v error=%v", paths, err)
	}
	manifests := make([]fixtureManifest, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture manifest %s: %v", path, err)
		}
		var manifest fixtureManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("decode fixture manifest %s: %v", path, err)
		}
		versionDirectory := filepath.Base(filepath.Dir(path))
		if manifest.FixtureSchemaVersion != 1 || manifest.Platform != "windows" || manifest.CodexVersion != versionDirectory {
			t.Errorf("fixture manifest %s has inconsistent versioned envelope: %#v", path, manifest)
		}
		if manifest.Transport != "stdio-jsonrpc-jsonl" && manifest.Transport != "exec-jsonl" {
			t.Errorf("fixture manifest %s has unknown transport %q", path, manifest.Transport)
		}
		if manifest.Status != "passed" && manifest.Status != "failed-as-expected" {
			t.Errorf("fixture manifest %s has unknown status %q", path, manifest.Status)
		}
		if manifest.Status == "failed-as-expected" && manifest.FailureClass == "" {
			t.Errorf("fixture manifest %s omitted expected failure classification", path)
		}
		if manifest.Fixture == "" {
			t.Errorf("fixture manifest %s omitted fixture path", path)
		} else if _, err := os.Stat(filepath.Join(filepath.Dir(path), manifest.Fixture)); err != nil {
			t.Errorf("fixture manifest %s references missing fixture %q: %v", path, manifest.Fixture, err)
		}
		manifests = append(manifests, manifest)
	}
	return manifests
}

func assertScenarios(t *testing.T, transport, version string, required []string, passed map[string]map[string]map[string]bool) {
	t.Helper()
	for _, scenario := range required {
		if !passed[transport][version][scenario] {
			t.Errorf("supported %s version %s has no passing %s fixture", transport, version, scenario)
		}
	}
}

func codexFixtureRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	root := workingDirectory
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return filepath.Join(filepath.Dir(root), "probes", "codex-host", "fixtures")
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatalf("could not locate runtime root from %s", workingDirectory)
		}
		root = parent
	}
}
