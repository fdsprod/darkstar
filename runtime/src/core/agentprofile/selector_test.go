package agentprofile

import (
	"errors"
	"testing"
	"time"

	"darkstar/src/ports/provider"
)

const testFingerprint = "0000000000000000000000000000000000000000000000000000000000000000"

func TestSelectUsesProviderPreferenceBeforeCapabilityPreference(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	definition.Capabilities.Preferred = []string{"local_image_input", "resume"}
	definition.Providers.Preferred = []string{"preferred"}
	profile, err := New(definition)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Select(profile, []Candidate{
		candidate("feature-rich", provider.HealthAvailable, available("structured_output", "resume", "local_image_input")),
		candidate("preferred", provider.HealthAvailable, available("structured_output")),
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	selected, ok := result.(Selected)
	if !ok {
		t.Fatalf("Select() = %T, want Selected", result)
	}
	if selected.ProviderID != "preferred" {
		t.Fatalf("selected provider = %q, want preferred", selected.ProviderID)
	}
	if !equalStrings(selected.UnavailablePreferred, []string{"local_image_input", "resume"}) {
		t.Fatalf("unavailable preferences = %v", selected.UnavailablePreferred)
	}
}

func TestSelectFallsBackToMostCapableCompatibleProvider(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	definition.Capabilities.Preferred = []string{"local_image_input", "resume"}
	definition.Providers.Preferred = []string{"preferred-unhealthy"}
	definition.Permissions = Permissions{
		Workspace: provider.AccessWorkspaceWrite, Network: provider.NetworkRestricted,
		Interaction: InteractionPermissions{Command: provider.InteractionAsk, File: provider.InteractionAsk, Tool: provider.InteractionDeny},
	}
	profile, err := New(definition)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Select(profile, []Candidate{
		candidate("preferred-unhealthy", provider.HealthUnavailable, available("structured_output", "workspace_write", "interactions", "resume", "local_image_input")),
		candidate("missing-write", provider.HealthAvailable, available("structured_output", "interactions", "resume", "local_image_input")),
		candidate("compatible-poor", provider.HealthAvailable, available("structured_output", "workspace_write", "interactions", "resume")),
		candidate("compatible-rich", provider.HealthDegraded, available("structured_output", "workspace_write", "interactions", "resume", "local_image_input")),
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	selected := result.(Selected)
	if selected.ProviderID != "compatible-rich" {
		t.Fatalf("selected provider = %q, want compatible-rich", selected.ProviderID)
	}
	if len(selected.UnavailablePreferred) != 0 {
		t.Fatalf("unavailable preferences = %v", selected.UnavailablePreferred)
	}
	if selected.Evaluations[0].ProviderID != "compatible-rich" || !selected.Evaluations[0].Compatible() {
		t.Fatalf("first evaluation = %#v", selected.Evaluations[0])
	}
	missingWrite := evaluationFor(t, selected.Evaluations, "missing-write")
	if !hasCapabilityRejection(missingWrite, CapabilityWorkspaceWrite) {
		t.Fatalf("missing-write evaluation = %#v", missingWrite)
	}
}

func TestSelectEnforcesAllowlistBeforeCompatibility(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	definition.Providers.Eligibility = AllowlistedProviders{ProviderIDs: []string{"allowed"}}
	profile, err := New(definition)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Select(profile, []Candidate{
		candidate("denied", provider.HealthAvailable, available("structured_output", "resume")),
		candidate("allowed", provider.HealthUnavailable, available("structured_output", "resume")),
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	unavailable, ok := result.(Unavailable)
	if !ok {
		t.Fatalf("Select() = %T, want Unavailable", result)
	}
	denied := evaluationFor(t, unavailable.Evaluations, "denied")
	if len(denied.Rejections) != 1 || denied.Rejections[0].Code != RejectionPolicyDenied {
		t.Fatalf("denied evaluation = %#v", denied)
	}
	allowed := evaluationFor(t, unavailable.Evaluations, "allowed")
	if len(allowed.Rejections) != 1 || allowed.Rejections[0].Code != RejectionHealthUnavailable {
		t.Fatalf("allowed evaluation = %#v", allowed)
	}
}

func TestSelectRejectsInvalidObservationsAndCandidateSets(t *testing.T) {
	t.Parallel()
	profile, err := New(validDefinition())
	if err != nil {
		t.Fatal(err)
	}
	malformed := candidate("malformed", provider.HealthAvailable, available("structured_output"))
	malformed.Capabilities.Fingerprint = "not-a-digest"
	result, err := Select(profile, []Candidate{malformed})
	if err != nil {
		t.Fatalf("Select() malformed observation error = %v", err)
	}
	unavailable := result.(Unavailable)
	if got := unavailable.Evaluations[0].Rejections; len(got) != 1 || got[0].Code != RejectionObservationInvalid {
		t.Fatalf("malformed observation rejections = %#v", got)
	}

	duplicate := candidate("same", provider.HealthAvailable, available("structured_output"))
	if _, err := Select(profile, []Candidate{duplicate, duplicate}); !errors.Is(err, ErrInvalidCandidates) {
		t.Fatalf("duplicate candidates error = %v", err)
	}
	if _, err := Select(Profile{}, []Candidate{duplicate}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("zero profile error = %v", err)
	}
}

func TestSelectedManifestIsDefensivelyCopied(t *testing.T) {
	t.Parallel()
	profile, err := New(validDefinition())
	if err != nil {
		t.Fatal(err)
	}
	input := candidate("provider", provider.HealthAvailable, available("structured_output", "resume"))
	capability := input.Capabilities.Features["structured_output"].(provider.AvailableCapability)
	capability.Metadata = map[string]string{"transport": "app-server"}
	input.Capabilities.Features["structured_output"] = capability

	result, err := Select(profile, []Candidate{input})
	if err != nil {
		t.Fatal(err)
	}
	selected := result.(Selected)
	input.Capabilities.Features["resume"] = provider.UnavailableCapability{Reason: "mutated"}
	selectedCapability := selected.CapabilityManifest.Features["structured_output"].(provider.AvailableCapability)
	capability.Metadata["transport"] = "mutated"
	if _, ok := selected.CapabilityManifest.Features["resume"].(provider.AvailableCapability); !ok {
		t.Fatal("selected manifest changed with candidate map")
	}
	if selectedCapability.Metadata["transport"] != "app-server" {
		t.Fatal("selected manifest metadata was not copied")
	}
}

func candidate(id string, healthState provider.HealthState, features map[string]provider.Capability) Candidate {
	return Candidate{
		ID:           id,
		Health:       provider.Health{State: healthState, Provider: "codex", ProviderVersion: "1.0.0"},
		Capabilities: provider.CapabilityManifest{Provider: "codex", Fingerprint: testFingerprint, Features: features, ObservedAt: time.Unix(1, 0).UTC()},
	}
}

func available(names ...string) map[string]provider.Capability {
	result := make(map[string]provider.Capability, len(names))
	for _, name := range names {
		result[name] = provider.AvailableCapability{Version: "v1"}
	}
	return result
}

func evaluationFor(t *testing.T, evaluations []Evaluation, providerID string) Evaluation {
	t.Helper()
	for _, evaluation := range evaluations {
		if evaluation.ProviderID == providerID {
			return evaluation
		}
	}
	t.Fatalf("no evaluation for %q", providerID)
	return Evaluation{}
}

func hasCapabilityRejection(evaluation Evaluation, capability string) bool {
	for _, rejection := range evaluation.Rejections {
		if rejection.Code == RejectionCapabilityMissing && rejection.Capability == capability {
			return true
		}
	}
	return false
}
