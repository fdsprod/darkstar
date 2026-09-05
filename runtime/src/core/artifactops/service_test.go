package artifactops

import (
	"encoding/json"
	"testing"
)

func TestTextDiffWireShapeIsClosedByStatus(t *testing.T) {
	valid, err := json.Marshal(TextDiff{Status: "unavailable", Reason: "unsupported"})
	if err != nil || string(valid) != `{"status":"unavailable","reason":"unsupported"}` {
		t.Fatalf("valid unavailable encoding = %s, %v", valid, err)
	}
	for _, invalid := range []TextDiff{
		{},
		{Status: "unavailable", Reason: "unknown"},
		{Status: "unavailable", Reason: "withheld", NextCursor: "impossible"},
		{Status: "available"},
	} {
		if _, err := json.Marshal(invalid); err == nil {
			t.Fatalf("invalid text diff marshaled: %#v", invalid)
		}
	}
}
