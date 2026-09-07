package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparationPolicyReloadsProjectOverridesAndRejectsMalformedPolicy(t *testing.T) {
	root := t.TempDir()
	paths := providerTestPaths(root)
	if err := os.MkdirAll(paths.Config, 0700); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(root, ".darkstar")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Config, "config.yaml"), []byte("routing:\n  assessment:\n    version: company-v1\n    requiredNodes: [review]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(projectDir, "config.yaml")
	if err := os.WriteFile(projectFile, []byte("routing:\n  assessment:\n    requiredNodes: [test]\n    allowedAssumptions: [approved design]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := configuredPreparationPolicy(paths, root)
	if err != nil || policy.Version != "company-v1" || len(policy.RequiredNodes) != 1 || policy.RequiredNodes[0] != "test" || len(policy.AllowedAssumptions) != 1 {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
	for _, body := range []string{"unknown: true", "version: ''", "requiredNodes: [bad-node]", "requiredNodes: [test, test]", "allowedAssumptions: [true]", "consequentialNodes: null"} {
		if err := os.WriteFile(projectFile, []byte("routing:\n  assessment:\n    "+body+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := configuredPreparationPolicy(paths, root); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}
