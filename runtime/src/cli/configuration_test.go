package cli

import (
	"strings"
	"testing"

	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
)

func TestParseConfigurationMutationKeepsScopeValueAndRevisionTyped(t *testing.T) {
	t.Parallel()
	revision := strings.Repeat("a", 64)
	request, key, err := parseConfigurationMutation([]string{"set", "--key", "provider.codex.actionAvailability", "--value-type", "enum", "--value", "disabled", "--revision", revision, "--project", "project_01K3Z1C2AAAAAAAAAAAAAAAAAA", "--idempotency-key", "configuration-key"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Scope.Kind() != config.MutationScopeProject || request.Scope.ProjectID() != "project_01K3Z1C2AAAAAAAAAAAAAAAAAA" || request.ExpectedRevision != revision || request.Change.Operation() != configmutation.ChangeSet || key != "configuration-key" {
		t.Fatalf("parsed = %#v key=%q", request, key)
	}
	value, ok := request.Change.Value()
	if !ok || value.Type() != config.SettingEnum || value.Value() != "disabled" {
		t.Fatalf("value = %#v, %v", value, ok)
	}
}
func TestParseConfigurationMutationRejectsContradictions(t *testing.T) {
	t.Parallel()
	revision := strings.Repeat("a", 64)
	cases := [][]string{{"unset", "--key", "x", "--value-type", "string", "--value", "oops", "--revision", revision}, {"preview", "--key", "x", "--unset", "--revision", revision, "--idempotency-key", "not-allowed"}, {"set", "--key", "x", "--value-type", "boolean", "--value", "sometimes", "--revision", revision}}
	for _, args := range cases {
		if _, _, err := parseConfigurationMutation(args); err == nil {
			t.Fatalf("accepted %#v", args)
		}
	}
}
