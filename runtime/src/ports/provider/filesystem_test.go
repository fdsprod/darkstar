package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scopedReadFixture(t *testing.T) ScopedReadRequirement {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ScopedReadRequirement{ReadRoots: []string{root}, ScopeDigest: strings.Repeat("a", 64), ConfigurationDigest: strings.Repeat("b", 64), EvidenceDigest: strings.Repeat("c", 64)}
}

func TestScopedReadRequiresEnforcedVersionedCapability(t *testing.T) {
	requirement := scopedReadFixture(t)
	for _, manifest := range []CapabilityManifest{
		{},
		{Features: map[string]Capability{CapabilityScopedReadFilesystem: UnavailableCapability{Reason: "unconfined"}}},
		{Features: map[string]Capability{CapabilityScopedReadFilesystem: AvailableCapability{Version: "v0"}}},
		{Features: map[string]Capability{"workspace_write": AvailableCapability{Version: "sandbox"}}},
	} {
		if err := ValidateFilesystemRequirement(&requirement, manifest); !errors.Is(err, ErrScopedReadUnsupported) {
			t.Fatalf("unsupported manifest accepted: %v", err)
		}
		if err := ValidateFilesystemRequirement(nil, manifest); err != nil {
			t.Fatalf("legacy request changed: %v", err)
		}
	}
	manifest := CapabilityManifest{Features: map[string]Capability{CapabilityScopedReadFilesystem: AvailableCapability{Version: ScopedReadFilesystemVersion}}}
	if err := ValidateFilesystemRequirement(&requirement, manifest); err != nil {
		t.Fatal(err)
	}
	requirement.ScopeDigest = "mutable"
	if err := ValidateFilesystemRequirement(&requirement, manifest); err == nil {
		t.Fatal("unbound requirement accepted")
	}
}

func TestScopedReadPathsCannotBroadenFrozenSubset(t *testing.T) {
	requirement := scopedReadFixture(t)
	allowed := filepath.Join(requirement.ReadRoots[0], "selected.txt")
	if err := os.WriteFile(allowed, []byte("selected snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireScopedReadPath(requirement, allowed); err != nil {
		t.Fatal(err)
	}
	other := scopedReadFixture(t)
	for _, path := range []string{other.ReadRoots[0], filepath.Dir(requirement.ReadRoots[0]), "selected.txt", requirement.ReadRoots[0] + string(filepath.Separator) + ".."} {
		if err := RequireScopedReadPath(requirement, path); err == nil {
			t.Fatalf("read outside subset accepted: %s", path)
		}
	}
	requirement.ReadRoots = []string{}
	if err := requirement.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := RequireScopedReadPath(requirement, allowed); err == nil {
		t.Fatal("empty read ceiling granted a read")
	}
}

func TestScopedReadRejectsSymlinkTraversal(t *testing.T) {
	requirement := scopedReadFixture(t)
	outside := scopedReadFixture(t)
	link := filepath.Join(requirement.ReadRoots[0], "escape")
	if err := os.Symlink(outside.ReadRoots[0], link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := RequireScopedReadPath(requirement, link); err == nil {
		t.Fatal("symlink escape accepted")
	}
	requirement.ReadRoots = []string{link}
	if err := requirement.Validate(); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestScopedReadDigestBindsRootsScopeConfigurationAndEvidence(t *testing.T) {
	requirement := scopedReadFixture(t)
	initial := requirement.Digest()
	for _, mutate := range []func(*ScopedReadRequirement){
		func(r *ScopedReadRequirement) {
			r.ReadRoots = []string{}
		},
		func(r *ScopedReadRequirement) {
			r.ScopeDigest = strings.Repeat("d", 64)
		},
		func(r *ScopedReadRequirement) {
			r.ConfigurationDigest = strings.Repeat("d", 64)
		},
		func(r *ScopedReadRequirement) {
			r.EvidenceDigest = strings.Repeat("d", 64)
		},
	} {
		changed := requirement
		mutate(&changed)
		if initial == changed.Digest() {
			t.Fatal("filesystem authority changed without changing its digest")
		}
	}
}
