package architecture_test

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/fdsprod/darkstar/runtime"

func TestRequiredPortPackagesExist(t *testing.T) {
	t.Parallel()

	root := runtimeRoot(t)
	for _, name := range []string{
		"artifactstore",
		"contentprocessor",
		"delivery",
		"executor",
		"platform",
		"provider",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entries, err := os.ReadDir(filepath.Join(root, "src", "ports", name))
			if err != nil {
				t.Fatalf("read port package %q: %v", name, err)
			}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
					return
				}
			}
			t.Fatalf("port package %q contains no Go source", name)
		})
	}
}

func TestRuntimePackageDependencies(t *testing.T) {
	t.Parallel()

	root := runtimeRoot(t)
	sourceRoot := filepath.Join(root, "src")
	err := filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			t.Errorf("rel path for %s: %v", path, err)
			return nil
		}
		sourcePackage := filepath.ToSlash(rel)
		validateConcreteLocation(t, sourcePackage, entry.Name())

		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import %s in %s: %v", spec.Path.Value, path, err)
				continue
			}
			if reason := dependencyViolation(sourcePackage, importPath); reason != "" {
				t.Errorf("%s imports %q: %s", fileSet.Position(spec.Pos()), importPath, reason)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk runtime source: %v", err)
	}
}

func TestDependencyRuleExamples(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		dependency string
		allowed    bool
	}{
		{"core to standard library", "src/core/workflow", "context", true},
		{"core to port", "src/core/workflow", modulePath + "/src/ports/executor", true},
		{"core to core", "src/core/workflow", modulePath + "/src/core/model", true},
		{"core to adapter", "src/core/workflow", modulePath + "/src/adapters/provider/codex", false},
		{"core to concrete platform", "src/core/workflow", modulePath + "/src/platform/windows", false},
		{"core to daemon", "src/core/workflow", modulePath + "/src/daemon", false},
		{"core to third party", "src/core/workflow", "example.com/library", false},
		{"port to sibling port", "src/ports/executor", modulePath + "/src/ports", true},
		{"port to core", "src/ports/provider", modulePath + "/src/core", false},
		{"port to adapter", "src/ports/provider", modulePath + "/src/adapters/provider/codex", false},
		{"port to module package outside ports", "src/ports/provider", modulePath + "/internal/shared", false},
		{"adapter to port", "src/adapters/provider/codex", modulePath + "/src/ports/provider", true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			allowed := dependencyViolation(test.source, test.dependency) == ""
			if allowed != test.allowed {
				t.Fatalf("allowed = %v, want %v", allowed, test.allowed)
			}
		})
	}
}

func dependencyViolation(sourcePackage, importPath string) string {
	protected := inPackageTree(sourcePackage, "src/core") || inPackageTree(sourcePackage, "src/ports")
	if !protected {
		return ""
	}

	if !strings.HasPrefix(importPath, modulePath+"/") {
		if strings.Contains(strings.SplitN(importPath, "/", 2)[0], ".") {
			return "core and port packages may not import third-party packages"
		}
		return ""
	}

	importedPackage := strings.TrimPrefix(importPath, modulePath+"/")

	switch {
	case inPackageTree(sourcePackage, "src/core"):
		if inPackageTree(importedPackage, "src/core") || inPackageTree(importedPackage, "src/ports") {
			return ""
		}
		return "core packages may import only core and application-owned ports"
	case inPackageTree(sourcePackage, "src/ports"):
		if inPackageTree(importedPackage, "src/ports") {
			return ""
		}
		return "port packages may import only other port packages"
	default:
		return ""
	}
}

func inPackageTree(packagePath, root string) bool {
	return packagePath == root || strings.HasPrefix(packagePath, root+"/")
}

func validateConcreteLocation(t *testing.T, sourcePackage, fileName string) {
	t.Helper()

	if sourcePackage == "src/adapters" && fileName != "doc.go" {
		t.Errorf("%s/%s: concrete adapter code must live under adapters/<port>/<implementation>", sourcePackage, fileName)
	}
	if strings.HasPrefix(sourcePackage, "src/adapters/") {
		parts := strings.Split(sourcePackage, "/")
		if len(parts) < 4 {
			t.Errorf("%s/%s: concrete adapter code must live under adapters/<port>/<implementation>", sourcePackage, fileName)
		}
	}
	if sourcePackage == "src/platform" && fileName != "doc.go" {
		t.Errorf("%s/%s: concrete platform code must live under platform/<os>", sourcePackage, fileName)
	}
}

func runtimeRoot(t *testing.T) string {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for candidate := workingDirectory; ; candidate = filepath.Dir(candidate) {
		if hasModulePath(filepath.Join(candidate, "go.mod"), modulePath) {
			return candidate
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			t.Fatalf("could not find %s go.mod from %s", modulePath, workingDirectory)
		}
	}
}

func hasModulePath(path, want string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		return len(fields) == 2 && fields[0] == "module" && fields[1] == want
	}
	return false
}
