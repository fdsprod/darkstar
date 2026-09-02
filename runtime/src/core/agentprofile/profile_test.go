package agentprofile

import (
	"errors"
	"testing"
	"time"

	"darkstar/src/ports/provider"
)

func TestProfileCanonicalizesSetsAndFreezesDefinition(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	definition.Capabilities.Required = []string{"workspace_write", "structured_output"}
	definition.Capabilities.Preferred = []string{"resume", "local_image_input"}
	definition.Providers.Eligibility = AllowlistedProviders{ProviderIDs: []string{"secondary", "primary"}}
	definition.Providers.Preferred = []string{"primary", "secondary"}

	profile, err := New(definition)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	canonical := profile.Definition()
	if got := canonical.Capabilities.Required; !equalStrings(got, []string{"structured_output", "workspace_write"}) {
		t.Fatalf("required capabilities = %v", got)
	}
	if got := canonical.Capabilities.Preferred; !equalStrings(got, []string{"local_image_input", "resume"}) {
		t.Fatalf("preferred capabilities = %v", got)
	}
	if got := canonical.Providers.Preferred; !equalStrings(got, []string{"primary", "secondary"}) {
		t.Fatalf("provider preference order = %v", got)
	}
	allowlist := canonical.Providers.Eligibility.(AllowlistedProviders)
	if !equalStrings(allowlist.ProviderIDs, []string{"primary", "secondary"}) {
		t.Fatalf("allowlist = %v", allowlist.ProviderIDs)
	}
	if len(profile.Fingerprint()) != 64 {
		t.Fatalf("fingerprint = %q", profile.Fingerprint())
	}

	definition.Capabilities.Required[0] = "mutated"
	allowlist.ProviderIDs[0] = "mutated"
	canonical.Capabilities.Required[0] = "mutated"
	canonical.Providers.Preferred[0] = "mutated"
	again := profile.Definition()
	if again.Capabilities.Required[0] != "structured_output" || again.Providers.Preferred[0] != "primary" {
		t.Fatalf("profile definition was mutated: %#v", again)
	}
}

func TestProfileFingerprintIgnoresSetDeclarationOrder(t *testing.T) {
	t.Parallel()
	first := validDefinition()
	first.Capabilities.Required = []string{"structured_output", "text_input"}
	first.Providers.Eligibility = AllowlistedProviders{ProviderIDs: []string{"b", "a"}}
	second := validDefinition()
	second.Capabilities.Required = []string{"text_input", "structured_output"}
	second.Providers.Eligibility = AllowlistedProviders{ProviderIDs: []string{"a", "b"}}

	left, err := New(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := New(second)
	if err != nil {
		t.Fatal(err)
	}
	if left.Fingerprint() != right.Fingerprint() {
		t.Fatalf("equivalent definitions have different fingerprints: %s != %s", left.Fingerprint(), right.Fingerprint())
	}

	second.Providers.Preferred = []string{"b", "a"}
	ranked, err := New(second)
	if err != nil {
		t.Fatal(err)
	}
	if ranked.Fingerprint() == right.Fingerprint() {
		t.Fatal("provider preference order did not affect fingerprint")
	}
}

func TestProfileRejectsContradictoryOrUnboundedDefinitions(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Definition){
		"missing identity":   func(value *Definition) { value.ID = "" },
		"untrimmed role":     func(value *Definition) { value.Role.Name = " builder " },
		"duplicate required": func(value *Definition) { value.Capabilities.Required = []string{"resume", "resume"} },
		"required preferred overlap": func(value *Definition) {
			value.Capabilities.Required = []string{"resume"}
			value.Capabilities.Preferred = []string{"resume"}
		},
		"missing eligibility": func(value *Definition) { value.Providers.Eligibility = nil },
		"empty allowlist":     func(value *Definition) { value.Providers.Eligibility = AllowlistedProviders{} },
		"preferred outside allowlist": func(value *Definition) {
			value.Providers.Eligibility = AllowlistedProviders{ProviderIDs: []string{"primary"}}
			value.Providers.Preferred = []string{"other"}
		},
		"invalid permission":   func(value *Definition) { value.Permissions.Network = provider.NetworkPolicy("sometimes") },
		"unbounded context":    func(value *Definition) { value.Limits.ContextTokens = 0 },
		"unbounded output":     func(value *Definition) { value.Limits.OutputTokens = 0 },
		"unbounded timeout":    func(value *Definition) { value.Limits.Timeout = 0 },
		"missing cancel grace": func(value *Definition) { value.Limits.CancellationGrace = 0 },
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			definition := validDefinition()
			mutate(&definition)
			if _, err := New(definition); !errors.Is(err, ErrInvalidProfile) {
				t.Fatalf("New() error = %v, want ErrInvalidProfile", err)
			}
		})
	}
}

func validDefinition() Definition {
	return Definition{
		ID: "builder", Version: "v1", Role: Role{Name: "builder", Instructions: "Implement the approved point."},
		Capabilities: CapabilityRequirements{Required: []string{"structured_output"}, Preferred: []string{"resume"}},
		Providers:    ProviderPolicy{Eligibility: AnyCompatible{}, Preferred: []string{}},
		Permissions: Permissions{
			Workspace: provider.AccessReadOnly, Network: provider.NetworkDenied,
			Interaction: InteractionPermissions{Command: provider.InteractionDeny, File: provider.InteractionDeny, Tool: provider.InteractionDeny},
		},
		Limits: Limits{ContextTokens: 32_000, OutputTokens: 4_000, CostUnits: 0, Timeout: 10 * time.Minute, CancellationGrace: 5 * time.Second},
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
