// Package artifacts resolves route evidence only through immutable registries.
package artifacts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/artifactstore"
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/representationregistry"
	"darkstar/src/ports/routeadvisor"
)

const MaxEvidenceBytes = 64 * 1024

var ErrUnavailable = routeadvisor.ErrEvidenceUnavailable

// References pin both the artifact version and its exact derived representation:
// artifact:<artifactId>@<positiveVersion>#<representationId>. Paths, URLs and
// mutable latest-version aliases are deliberately outside this contract.
var referencePattern = regexp.MustCompile(`^artifact:([A-Za-z0-9_-]+)@([1-9][0-9]*)#([A-Za-z0-9_-]+)$`)

type ArtifactReader interface {
	ArtifactVersion(context.Context, artifactregistry.VersionRef) (artifactregistry.ArtifactVersion, error)
}
type RepresentationReader interface {
	Representation(context.Context, string) (representationregistry.Representation, error)
}
type ContentReader interface {
	Open(context.Context, artifactstore.OpenRequest) (io.ReadCloser, error)
}

type Resolver struct {
	Artifacts       ArtifactReader
	Representations RepresentationReader
	Store           ContentReader
}

func (r Resolver) Resolve(ctx context.Context, reference string) (routeadvisor.Evidence, error) {
	unavailable := routeadvisor.Evidence{Reference: reference}
	parts := referencePattern.FindStringSubmatch(reference)
	if parts == nil || r.Artifacts == nil || r.Representations == nil || r.Store == nil {
		return unavailable, ErrUnavailable
	}
	version, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return unavailable, ErrUnavailable
	}
	ref := artifactregistry.VersionRef{ArtifactID: parts[1], Version: version}
	artifact, err := r.Artifacts.ArtifactVersion(ctx, ref)
	if err != nil {
		return unavailable, fmt.Errorf("%w: artifact lookup failed", ErrUnavailable)
	}
	if artifact.ArtifactID != ref.ArtifactID || artifact.Version != ref.Version || artifact.Status != artifactregistry.StatusStored || (artifact.Sensitivity != artifactregistry.SensitivityPublic && artifact.Sensitivity != artifactregistry.SensitivityInternal) {
		return unavailable, ErrUnavailable
	}
	representation, err := r.Representations.Representation(ctx, parts[3])
	if err != nil {
		return unavailable, fmt.Errorf("%w: representation lookup failed", ErrUnavailable)
	}
	if representation.RepresentationID != parts[3] || representation.Artifact != ref || representation.Truncated || representation.Size <= 0 || representation.Size > MaxEvidenceBytes {
		return unavailable, ErrUnavailable
	}
	if representation.Disclosure != representationregistry.DisclosureRaw && representation.Disclosure != representationregistry.DisclosureRedacted {
		return unavailable, ErrUnavailable
	}
	if representation.Kind != contentprocessor.RepresentationText && representation.Kind != contentprocessor.RepresentationPreview {
		return unavailable, ErrUnavailable
	}
	mediaType, parameters, err := mime.ParseMediaType(representation.MediaType)
	if err != nil || (mediaType != "text/plain" && mediaType != "text/markdown") || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		return unavailable, ErrUnavailable
	}
	reader, err := r.Store.Open(ctx, artifactstore.OpenRequest{Locator: representation.Locator, ExpectedDigest: representation.Digest})
	if err != nil {
		return unavailable, fmt.Errorf("%w: content lookup failed", ErrUnavailable)
	}
	defer func() {
		_ = reader.Close()
	}()
	content, err := io.ReadAll(io.LimitReader(reader, MaxEvidenceBytes+1))
	if err != nil || int64(len(content)) != representation.Size || len(content) > MaxEvidenceBytes || !utf8.Valid(content) || fmt.Sprintf("%x", sha256.Sum256(content)) != representation.Digest {
		return unavailable, ErrUnavailable
	}
	return routeadvisor.Evidence{Reference: reference, Digest: representation.Digest, Content: string(content)}, nil
}
