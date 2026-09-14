package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const CapabilityScopedReadFilesystem = "scoped_read_filesystem"
const ScopedReadFilesystemVersion = "v1"
const ScopedReadUnavailableReason = "This provider cannot enforce filesystem reads limited to frozen snapshot roots. Select a provider with scoped_read_filesystem v1 support; read-only sandbox mode and workspace root metadata do not provide read confinement."

var ErrScopedReadUnsupported = errors.New("scoped filesystem reads are unsupported")

// ScopedReadRequirement is an explicit read ceiling. Empty ReadRoots grants no
// filesystem reads. Roots contain only daemon-owned immutable material, never
// live repositories. Digests bind that material to frozen scope and configuration.
// A nil requirement preserves the older Access contract; it never means scoped.
type ScopedReadRequirement struct {
	ReadRoots           []string
	ScopeDigest         string
	ConfigurationDigest string
	EvidenceDigest      string
}

// Digest identifies the complete frozen requirement, independent of root order.
func (requirement ScopedReadRequirement) Digest() string {
	copy := requirement
	copy.ReadRoots = append([]string{}, requirement.ReadRoots...)
	sort.Strings(copy.ReadRoots)
	data, _ := json.Marshal(copy)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// ValidateFilesystemRequirement is admission validation, not an OS sandbox.
// An adapter may advertise support only when every native and hosted read path
// enforces this ceiling and writes are denied. A metadata allowlist is insufficient.
func ValidateFilesystemRequirement(requirement *ScopedReadRequirement, manifest CapabilityManifest) error {
	if requirement == nil {
		return nil
	}
	capability, ok := manifest.Features[CapabilityScopedReadFilesystem].(AvailableCapability)
	if !ok || capability.Version != ScopedReadFilesystemVersion {
		return fmt.Errorf("%w: %s", ErrScopedReadUnsupported, ScopedReadUnavailableReason)
	}
	return requirement.Validate()
}

func (requirement ScopedReadRequirement) Validate() error {
	for _, digest := range []string{requirement.ScopeDigest, requirement.ConfigurationDigest, requirement.EvidenceDigest} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
			return errors.New("scoped filesystem reads require frozen scope, configuration, and evidence SHA-256 digests")
		}
	}
	seen := map[string]bool{}
	for _, root := range requirement.ReadRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || seen[root] {
			return errors.New("scoped filesystem read roots must be distinct clean absolute paths")
		}
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil || canonical != root {
			return errors.New("scoped filesystem read roots must resolve to their frozen canonical paths")
		}
		seen[root] = true
	}
	return nil
}

// RequireScopedReadPath checks a hosted read before dispatch. It does not make
// an arbitrary provider process confined. Callers must retain immutable roots
// and use a race-safe filesystem reader; a path check cannot prevent TOCTOU.
func RequireScopedReadPath(requirement ScopedReadRequirement, path string) error {
	if err := requirement.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("scoped read requires a clean absolute path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return errors.New("scoped read rejects unresolved paths or symbolic-link traversal")
	}
	for _, root := range requirement.ReadRoots {
		relative, err := filepath.Rel(root, canonical)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return nil
		}
	}
	return errors.New("filesystem read is outside the frozen snapshot roots")
}
