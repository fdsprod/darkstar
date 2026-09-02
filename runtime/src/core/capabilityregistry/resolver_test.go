package capabilityregistry

import (
	"errors"
	"strings"
	"testing"
	"time"

	registryport "darkstar/src/ports/capabilityregistry"
)

func TestResolveUsesProvenanceFallbacksAndFreezesAuditFields(t *testing.T) {
	t.Parallel()
	records := []registryport.Record{
		record("cap_registered", "mcp:docs/search", registryport.KindTool, registryport.ClassRegistered, "2.0.0", nil),
		record("cap_inherited", "mcp:docs/search", registryport.KindTool, registryport.ClassInherited, "", nil),
		record("cap_fallback", "darkstar:artifact.read", registryport.KindTool, registryport.ClassGuaranteed, "1.0.0", []string{"darkstar:workspace.read"}),
		record("cap_dependency", "darkstar:workspace.read", registryport.KindTool, registryport.ClassGuaranteed, "1.0.0", nil),
	}
	resolver, err := New(records)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := resolver.Resolve(Request{
		Requirements: []Requirement{
			{Name: "mcp:docs/search", Kind: registryport.KindTool, Mode: RequirementRequired, Version: "2.0.0"},
			{Name: "project:missing", Kind: registryport.KindTool, Mode: RequirementPreferred, Fallbacks: []Alternative{{Name: "darkstar:artifact.read"}}},
		},
		Grants: map[string]Grant{
			"cap_registered": {Decision: PolicyAllow, Permissions: []string{"network.read"}},
			"cap_fallback":   {Decision: PolicyAllow, Permissions: []string{"artifact.read"}},
			"cap_dependency": {Decision: PolicyAllow, Permissions: []string{"workspace.read"}},
		},
		PolicyDigest: strings.Repeat("a", 64), HostFingerprint: "codex/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Selections) != 3 || manifest.Selections[2].ID != "cap_registered" {
		t.Fatalf("selections = %#v", manifest.Selections)
	}
	if manifest.Selections[2].Source.Locator != "test/cap_registered" || manifest.Selections[2].Version != "2.0.0" || len(manifest.Selections[2].Permissions) != 1 {
		t.Fatalf("audit fields missing: %#v", manifest.Selections[2])
	}
	if !manifest.Degraded() || len(manifest.FallbacksUsed) != 1 || len(manifest.Digest) != 64 {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestResolveFailsClosedAtEveryBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		records     []registryport.Record
		requirement Requirement
		grants      map[string]Grant
		code        FailureCode
	}{
		{"missing", nil, Requirement{Name: "project:absent", Kind: registryport.KindSkill}, nil, FailureRequiredMissing},
		{"version", []registryport.Record{record("cap", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.0.0", nil)}, Requirement{Name: "project:review", Kind: registryport.KindSkill, Version: "2.0.0"}, nil, FailureVersionMismatch},
		{"fingerprint", []registryport.Record{record("cap", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.0.0", nil)}, Requirement{Name: "project:review", Kind: registryport.KindSkill, Fingerprint: strings.Repeat("b", 64)}, nil, FailureFingerprintChanged},
		{"policy", []registryport.Record{record("cap", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.0.0", nil)}, Requirement{Name: "project:review", Kind: registryport.KindSkill}, map[string]Grant{"cap": {Decision: PolicyDeny}}, FailurePolicyDenied},
		{"unhealthy", []registryport.Record{unhealthy(record("cap", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.0.0", nil))}, Requirement{Name: "project:review", Kind: registryport.KindSkill}, nil, FailureUnhealthy},
		{"inherited", []registryport.Record{record("cap", "codex-inherited:project/review", registryport.KindSkill, registryport.ClassInherited, "", nil)}, Requirement{Name: "codex-inherited:project/review", Kind: registryport.KindSkill}, nil, FailureInheritedNotAllowed},
		{"dependency", []registryport.Record{record("cap", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.0.0", []string{"mcp:docs/search"})}, Requirement{Name: "project:review", Kind: registryport.KindSkill}, nil, FailureDependencyMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver, err := New(test.records)
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.Resolve(Request{Requirements: []Requirement{test.requirement}, Grants: test.grants, PolicyDigest: "policy", HostFingerprint: "host"})
			var failure *ResolutionError
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestNewRejectsSameClassShadowing(t *testing.T) {
	t.Parallel()
	first := record("cap_a", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.0.0", nil)
	second := record("cap_b", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.1.0", nil)
	_, err := New([]registryport.Record{first, second})
	var failure *ResolutionError
	if !errors.As(err, &failure) || failure.Code != FailureAmbiguous {
		t.Fatalf("error = %v", err)
	}
}

func record(id, name string, kind registryport.Kind, class registryport.Class, version string, dependencies []string) registryport.Record {
	return registryport.Record{
		SchemaVersion: 1, ID: id, Name: name, Kind: kind, Class: class, DeclaredVersion: version,
		Fingerprint: strings.Repeat("a", 64), Source: registryport.Source{Type: "test", Locator: "test/" + id},
		Dependencies: dependencies, Availability: registryport.AvailabilityAvailable, ObservedAt: time.Unix(1, 0).UTC(),
	}
}

func unhealthy(value registryport.Record) registryport.Record {
	value.Availability = registryport.AvailabilityUnhealthy
	return value
}
