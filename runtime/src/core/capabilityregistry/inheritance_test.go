package capabilityregistry

import (
	"errors"
	"strings"
	"testing"
	"time"

	registryport "darkstar/src/ports/capabilityregistry"
)

func TestInheritCapabilitiesRequiresExplicitPolicyAndPreservesUnversionedEvidence(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	project := observation("lint", ObservationProject, registryport.KindSkill, "", "a", observedAt)
	user := observation("review", ObservationUser, registryport.KindSkill, "", "b", observedAt)
	codex := observation("docs/search", ObservationCodex, registryport.KindTool, "2.0.0", "c", observedAt)

	snapshot, err := InheritCapabilities(InheritanceRequest{
		Observations: []Observation{user, codex, project},
		Rules: []EligibilityRule{
			{Name: "codex-inherited:user/review", Kind: registryport.KindSkill, Decision: PolicyAllow, Permissions: []string{"workspace.read"}},
			{Name: "codex-inherited:codex/docs/search", Kind: registryport.KindTool, Decision: PolicyDeny},
		},
		HostFingerprint: "codex/0.151.0-alpha.7.2/windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 3 || len(snapshot.Digest) != 64 || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Records[0].Name != "codex-inherited:codex/docs/search" || snapshot.Records[1].Name != "codex-inherited:project/lint" || snapshot.Records[2].Name != "codex-inherited:user/review" {
		t.Fatalf("records are not canonical: %#v", snapshot.Records)
	}
	if snapshot.Records[2].DeclaredVersion != "" {
		t.Fatalf("unversioned observation acquired a version: %#v", snapshot.Records[2])
	}
	if snapshot.Grants[snapshot.Records[1].ID].Decision != PolicyDeny {
		t.Fatalf("unconfigured project capability was not denied: %#v", snapshot.Grants)
	}

	resolver, err := New(snapshot.Records)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(Request{
		Requirements: []Requirement{{Name: "codex-inherited:project/lint", Kind: registryport.KindSkill, AcceptInherited: true}},
		Grants:       snapshot.Grants, PolicyDigest: snapshot.Digest, HostFingerprint: snapshot.HostFingerprint,
	})
	assertResolutionCode(t, err, FailurePolicyDenied)

	_, err = resolver.Resolve(Request{
		Requirements: []Requirement{{Name: "codex-inherited:user/review", Kind: registryport.KindSkill}},
		Grants:       snapshot.Grants, PolicyDigest: snapshot.Digest, HostFingerprint: snapshot.HostFingerprint,
	})
	assertResolutionCode(t, err, FailureInheritedNotAllowed)

	manifest, err := resolver.Resolve(Request{
		Requirements: []Requirement{{Name: "codex-inherited:user/review", Kind: registryport.KindSkill, AcceptInherited: true}},
		Grants:       snapshot.Grants, PolicyDigest: snapshot.Digest, HostFingerprint: snapshot.HostFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Selections) != 1 || manifest.Selections[0].Version != "" || !manifest.Degraded() {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestInheritCapabilitiesPromotesOnlyAnExactRegisteredBinding(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 9, 3, 2, 0, 0, 0, time.UTC)
	observed := observation("review", ObservationProject, registryport.KindSkill, "", "d", observedAt)
	registered := record("cap_project_review", "project:review", registryport.KindSkill, registryport.ClassRegistered, "1.4.0", nil)
	registered.Fingerprint = observed.Fingerprint
	registered.Source = observed.Source

	snapshot, err := InheritCapabilities(InheritanceRequest{
		Observations:    []Observation{observed},
		Registrations:   []registryport.Record{registered},
		Bindings:        map[string]string{"codex-inherited:project/review": registered.ID},
		Rules:           []EligibilityRule{{Name: registered.Name, Kind: registered.Kind, Decision: PolicyAllow, Permissions: []string{"workspace.read"}}},
		HostFingerprint: "host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 1 || snapshot.Records[0].ID != registered.ID || snapshot.Records[0].Class != registryport.ClassRegistered || snapshot.Records[0].DeclaredVersion != "1.4.0" {
		t.Fatalf("registered projection = %#v", snapshot.Records)
	}
	if snapshot.Grants[registered.ID].Decision != PolicyAllow || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("policy projection = %#v, diagnostics = %#v", snapshot.Grants, snapshot.Diagnostics)
	}

	changed := observed
	changed.Fingerprint = strings.Repeat("e", 64)
	mismatch, err := InheritCapabilities(InheritanceRequest{
		Observations: []Observation{changed}, Registrations: []registryport.Record{registered},
		Bindings:        map[string]string{"codex-inherited:project/review": registered.ID},
		Rules:           []EligibilityRule{{Name: "codex-inherited:project/review", Kind: registryport.KindSkill, Decision: PolicyAllow}},
		HostFingerprint: "host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Records[0].Class != registryport.ClassInherited || len(mismatch.Diagnostics) != 1 || mismatch.Diagnostics[0].Code != DiagnosticRegistrationMismatch {
		t.Fatalf("mismatch = %#v", mismatch)
	}
}

func TestInheritCapabilitiesIsDeterministicAndRejectsAmbiguousObservations(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	first := observation("one", ObservationProject, registryport.KindSkill, "", "1", observedAt)
	second := observation("two", ObservationUser, registryport.KindTool, "", "2", observedAt)
	rules := []EligibilityRule{
		{Name: "codex-inherited:user/two", Kind: registryport.KindTool, Decision: PolicyAllow},
		{Name: "codex-inherited:project/one", Kind: registryport.KindSkill, Decision: PolicyAllow},
	}
	left, err := InheritCapabilities(InheritanceRequest{Observations: []Observation{first, second}, Rules: rules, HostFingerprint: "host"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := InheritCapabilities(InheritanceRequest{Observations: []Observation{second, first}, Rules: []EligibilityRule{rules[1], rules[0]}, HostFingerprint: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest != right.Digest {
		t.Fatalf("digest changed with input ordering: %s != %s", left.Digest, right.Digest)
	}

	_, err = InheritCapabilities(InheritanceRequest{Observations: []Observation{first, first}, HostFingerprint: "host"})
	var failure *ResolutionError
	if !errors.As(err, &failure) || failure.Code != FailureAmbiguous {
		t.Fatalf("duplicate observation error = %v", err)
	}
}

func observation(name string, scope ObservationScope, kind registryport.Kind, version, fingerprintSeed string, observedAt time.Time) Observation {
	return Observation{
		Name: name, Kind: kind, Scope: scope, DeclaredVersion: version,
		Fingerprint:  strings.Repeat(fingerprintSeed, 64),
		Source:       registryport.Source{Type: "codex_inventory", Locator: string(scope) + "/" + name},
		Dependencies: []string{}, Availability: registryport.AvailabilityAvailable, ObservedAt: observedAt,
	}
}

func assertResolutionCode(t *testing.T, err error, want FailureCode) {
	t.Helper()
	var failure *ResolutionError
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("resolution error = %v, want %s", err, want)
	}
}
