package artifacts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/artifactstore"
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/representationregistry"
)

type fixture struct {
	artifact       artifactregistry.ArtifactVersion
	representation representationregistry.Representation
	content        string
	opens          int
	lookups        int
}

func (f *fixture) ArtifactVersion(context.Context, artifactregistry.VersionRef) (artifactregistry.ArtifactVersion, error) {
	f.lookups++
	return f.artifact, nil
}
func (f *fixture) Representation(context.Context, string) (representationregistry.Representation, error) {
	f.lookups++
	return f.representation, nil
}
func (f *fixture) Open(_ context.Context, request artifactstore.OpenRequest) (io.ReadCloser, error) {
	f.opens++
	if request.Locator != f.representation.Locator || request.ExpectedDigest != f.representation.Digest {
		return nil, fmt.Errorf("incorrect immutable content lookup")
	}
	return io.NopCloser(strings.NewReader(f.content)), nil
}
func newFixture() *fixture {
	content := "Approved design: retain validation and deliver the documentation update."
	return &fixture{
		artifact:       artifactregistry.ArtifactVersion{ArtifactID: "artifact_a", Version: 2, Status: artifactregistry.StatusStored, Sensitivity: artifactregistry.SensitivityInternal},
		representation: representationregistry.Representation{RepresentationID: "representation_b", Artifact: artifactregistry.VersionRef{ArtifactID: "artifact_a", Version: 2}, Kind: contentprocessor.RepresentationText, MediaType: "text/plain; charset=utf-8", Locator: "opaque-content", Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(content))), Size: int64(len(content)), Disclosure: representationregistry.DisclosureRaw},
		content:        content,
	}
}

func TestResolveUsesExactRegistryContent(t *testing.T) {
	for _, sensitivity := range []artifactregistry.Sensitivity{artifactregistry.SensitivityPublic, artifactregistry.SensitivityInternal} {
		f := newFixture()
		f.artifact.Sensitivity = sensitivity
		resolver := Resolver{Artifacts: f, Representations: f, Store: f}
		evidence, err := resolver.Resolve(context.Background(), "artifact:artifact_a@2#representation_b")
		if err != nil || evidence.Content != f.content || evidence.Digest != f.representation.Digest || f.opens != 1 {
			t.Fatalf("evidence = %#v, %v; opens=%d", evidence, err, f.opens)
		}
	}
}

func TestResolveRejectsUntrustedOrMutableReferencesBeforeLookup(t *testing.T) {
	for _, reference := range []string{"https://example.org/private", `C:\private.txt`, "../../secret", "artifact:artifact_a@latest#representation_b", "artifact:artifact_a@0#representation_b", "artifact:artifact_a@02#representation_b", "artifact:artifact_a@2", "artifact:artifact_a@18446744073709551616#representation_b"} {
		f := newFixture()
		resolver := Resolver{Artifacts: f, Representations: f, Store: f}
		evidence, err := resolver.Resolve(context.Background(), reference)
		if err == nil || evidence.Content != "" || evidence.Digest != "" || evidence.Reference != reference || f.lookups != 0 {
			t.Fatalf("%s: %#v, %v; lookups=%d", reference, evidence, err, f.lookups)
		}
	}
}

func TestResolveEnforcesDisclosureAndRepresentationIntegrity(t *testing.T) {
	cases := map[string]func(*fixture){
		"secret": func(f *fixture) {
			f.artifact.Sensitivity = artifactregistry.SensitivitySecret
		},
		"sensitive": func(f *fixture) {
			f.artifact.Sensitivity = artifactregistry.SensitivitySensitive
		},
		"unknown": func(f *fixture) {
			f.artifact.Sensitivity = artifactregistry.SensitivityUnknown
		},
		"quarantined": func(f *fixture) {
			f.artifact.Status = artifactregistry.StatusQuarantined
		},
		"uninspectable": func(f *fixture) {
			f.artifact.Status = artifactregistry.StatusStoredUninspectable
		},
		"wrong version": func(f *fixture) {
			f.representation.Artifact.Version = 1
		},
		"wrong representation": func(f *fixture) {
			f.representation.RepresentationID = "other"
		},
		"withheld": func(f *fixture) {
			f.representation.Disclosure = representationregistry.DisclosureWithheld
		},
		"unknown disclosure": func(f *fixture) {
			f.representation.Disclosure = ""
		},
		"binary": func(f *fixture) {
			f.representation.MediaType = "application/octet-stream"
		},
		"wrong charset": func(f *fixture) {
			f.representation.MediaType = "text/plain; charset=utf-16"
		},
		"truncated": func(f *fixture) {
			f.representation.Truncated = true
		},
		"oversized": func(f *fixture) {
			f.representation.Size = MaxEvidenceBytes + 1
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			mutate(f)
			resolver := Resolver{Artifacts: f, Representations: f, Store: f}
			evidence, err := resolver.Resolve(context.Background(), "artifact:artifact_a@2#representation_b")
			if err == nil || evidence.Content != "" || evidence.Digest != "" || f.opens != 0 {
				t.Fatalf("disallowed evidence read: %#v, %v; opens=%d", evidence, err, f.opens)
			}
		})
	}
	for name, mutate := range map[string]func(*fixture){
		"digest": func(f *fixture) {
			f.content = strings.Repeat("x", len(f.content))
		},
		"size": func(f *fixture) {
			f.content += "extra"
		},
		"utf8": func(f *fixture) {
			f.content = "\xff"
			f.representation.Size = 1
			f.representation.Digest = fmt.Sprintf("%x", sha256.Sum256([]byte(f.content)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			mutate(f)
			resolver := Resolver{Artifacts: f, Representations: f, Store: f}
			evidence, err := resolver.Resolve(context.Background(), "artifact:artifact_a@2#representation_b")
			if err == nil || evidence.Content != "" || evidence.Digest != "" {
				t.Fatalf("corrupt evidence accepted: %#v, %v", evidence, err)
			}
		})
	}
}
