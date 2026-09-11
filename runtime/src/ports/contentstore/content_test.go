package contentstore

import (
	"encoding/json"
	"testing"
)

func TestDocumentRejectsCrossedKindsAndUnknownConditions(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"template","content":"# Plan","instructions":""}`,
		`{"kind":"prompt","instructions":"Plan","content":""}`,
		`{"kind":"prompt","instructions":"Plan","sections":[{"id":"x","instructions":"Do","when":{"kind":"eval","input":"x"}}]}`,
		`{"kind":"prompt","instructions":"Plan","sections":[{"id":"x","instructions":"Do","when":{"kind":"revision","input":"x"}}]}`,
		`{"kind":"prompt","instructions":"Plan","execute":"shell"}`,
	} {
		var document Document
		if err := json.Unmarshal([]byte(raw), &document); err == nil {
			t.Fatalf("invalid closed document accepted: %s", raw)
		}
	}
	for _, version := range []string{"latest", "1", "v1.0", "01.0.0", ""} {
		if ValidateVersion(version) == nil {
			t.Fatalf("invalid version accepted: %q", version)
		}
	}
}
