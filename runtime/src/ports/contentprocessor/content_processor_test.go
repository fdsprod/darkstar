package contentprocessor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicWireFieldsUseLowerCamelCase(t *testing.T) {
	value := struct {
		Processor Descriptor `json:"processor"`
		Support   Support    `json:"support"`
	}{
		Processor: Descriptor{Name: "common", Version: "1", MediaTypes: []string{"text/plain"}},
		Support:   Support{State: SupportSupported, MediaType: "text/plain", Diagnostics: []string{}},
	}
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(content)
	for _, field := range []string{`"name"`, `"version"`, `"mediaTypes"`, `"state"`, `"mediaType"`, `"diagnostics"`} {
		if !strings.Contains(wire, field) {
			t.Fatalf("wire field %s missing from %s", field, wire)
		}
	}
	for _, field := range []string{`"Name"`, `"Version"`, `"MediaTypes"`, `"State"`, `"MediaType"`, `"Diagnostics"`} {
		if strings.Contains(wire, field) {
			t.Fatalf("Go field %s leaked into %s", field, wire)
		}
	}
}
