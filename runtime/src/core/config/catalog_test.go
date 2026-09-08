package config_test

import (
	"encoding/json"
	"testing"

	"darkstar/src/core/config"
)

func TestMutationScopeAndTypedValueRejectContradictoryShapes(t *testing.T) {
	t.Parallel()
	tests := []string{`{"type":"user","projectId":"project_x"}`, `{"type":"project"}`}
	for _, encoded := range tests {
		var scope config.MutationScope
		if json.Unmarshal([]byte(encoded), &scope) == nil {
			t.Fatalf("scope accepted %s", encoded)
		}
	}
	values := []string{`{"type":"boolean","value":"true"}`, `{"type":"path","value":3}`, `{"type":"future","value":"x"}`, `{"type":"string","value":"x","secret":"leak"}`}
	for _, encoded := range values {
		var value config.TypedValue
		if json.Unmarshal([]byte(encoded), &value) == nil {
			t.Fatalf("value accepted %s", encoded)
		}
	}
}

func TestCatalogDescribesScopesSensitivityRestartAndActions(t *testing.T) {
	t.Parallel()
	catalog := config.SupportedCatalog()
	if catalog.SchemaVersion != 1 || len(catalog.Settings) < 2 {
		t.Fatalf("catalog = %#v", catalog)
	}
	executable, ok := config.LookupSetting("provider.codex.executable")
	if !ok || executable.Type != config.SettingPath || executable.Sensitivity != config.SensitivityPublic || executable.Restart != config.RestartDaemon || len(executable.AllowedScopes) != 1 || executable.AllowedScopes[0] != config.MutationScopeUser || len(executable.Actions) != 3 {
		t.Fatalf("executable descriptor = %#v", executable)
	}
	secret, ok := config.LookupSetting("provider.codex.apiKey")
	if !ok || secret.Type != config.SettingSecretReference || secret.Sensitivity != config.SensitivitySecret {
		t.Fatalf("secret descriptor = %#v", secret)
	}
}

func TestQueueLimitIsBoundedAndLive(t *testing.T) {
	d, ok := config.LookupSetting("scheduler.maxConcurrentRuns")
	if !ok || d.Default.Value() != int64(3) || d.Restart != config.RestartNone || len(d.AllowedScopes) != 1 || d.AllowedScopes[0] != config.MutationScopeUser {
		t.Fatalf("queue descriptor: %#v", d)
	}
	for _, value := range []int64{0, -1, 33} {
		if d.ValidateValue(config.IntegerValue(value)) == nil {
			t.Fatalf("accepted limit %d", value)
		}
	}
	if err := d.ValidateValue(config.IntegerValue(3)); err != nil {
		t.Fatal(err)
	}
}
