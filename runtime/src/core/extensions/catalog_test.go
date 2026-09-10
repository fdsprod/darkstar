package extensions

import (
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/ports/extension"
)

func descriptor() extension.Descriptor {
	return extension.Descriptor{Ref: extension.Ref{ID: "example/check", Version: "1.0.0", Digest: strings.Repeat("a", 64)}, Protocol: extension.Protocol, ConfigurationSchema: json.RawMessage(`{"type":"object"}`)}
}

func TestPinnedCatalogRejectsReplacementAndMissingVersions(t *testing.T) {
	d := descriptor()
	c, err := New(Registration[string]{d, "original"})
	if err != nil {
		t.Fatal(err)
	}
	ref := d.Ref
	ref.Digest = strings.Repeat("b", 64)
	if _, err := c.Resolve(ref); err == nil {
		t.Fatal("accepted replacement digest")
	}
	ref = d.Ref
	ref.Version = "2.0.0"
	if _, err := c.Resolve(ref); err == nil {
		t.Fatal("floated to installed version")
	}
	replacement := d
	replacement.Ref.Digest = strings.Repeat("b", 64)
	if _, err := New(Registration[string]{d, "one"}, Registration[string]{replacement, "two"}); err == nil {
		t.Fatal("accepted conflicting identity")
	}
	d.ConfigurationSchema[0] = 'x'
	listed := c.Descriptors()
	listed[0].ConfigurationSchema[0] = 'y'
	if !json.Valid(c.Descriptors()[0].ConfigurationSchema) {
		t.Fatal("catalog descriptor was mutated")
	}
}

func TestIncompatibleProtocolFailsAtRegistration(t *testing.T) {
	d := descriptor()
	d.Protocol = "future"
	if _, err := New(Registration[int]{d, 1}); err == nil {
		t.Fatal("accepted incompatible protocol")
	}
}
