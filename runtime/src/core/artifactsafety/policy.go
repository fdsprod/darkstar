// Package artifactsafety defines fail-closed ingestion and derivation budgets.
package artifactsafety

import (
	"errors"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
)

const PolicyVersion = "artifact-safety/v1alpha1"

type DisclosurePolicy string

const (
	DisclosureClassifiedOnly DisclosurePolicy = "classified_only"
	DisclosureAllowSensitive DisclosurePolicy = "allow_sensitive"
	DisclosureAllowAll       DisclosurePolicy = "allow_all"
)

type Policy struct {
	Version           string
	SourceBytes       int64
	DecodedBytes      int64
	ExpandedBytes     int64
	TableCells        int
	PDFPages          int
	ImagePixels       int64
	Representations   int
	ProcessorWallTime time.Duration
	ProcessorMemory   int64
	Disclosure        DisclosurePolicy
}

func DefaultPolicy() Policy {
	return Policy{
		Version: PolicyVersion, SourceBytes: 25 << 20, DecodedBytes: 2 << 20, ExpandedBytes: 64 << 20,
		TableCells: 100_000, PDFPages: 200, ImagePixels: 40_000_000, Representations: 8,
		ProcessorWallTime: 30 * time.Second, ProcessorMemory: 256 << 20, Disclosure: DisclosureClassifiedOnly,
	}
}

func (policy Policy) Validate() error {
	if policy.Version == "" || policy.SourceBytes <= 0 || policy.DecodedBytes <= 0 || policy.ExpandedBytes <= 0 ||
		policy.TableCells <= 0 || policy.PDFPages <= 0 || policy.ImagePixels <= 0 || policy.Representations <= 0 ||
		policy.ProcessorWallTime <= 0 || policy.ProcessorMemory <= 0 {
		return errors.New("artifact safety policy requires positive versioned limits")
	}
	switch policy.Disclosure {
	case DisclosureClassifiedOnly, DisclosureAllowSensitive, DisclosureAllowAll:
		return nil
	default:
		return errors.New("artifact safety disclosure policy is invalid")
	}
}

func (policy Policy) ProcessorLimits() contentprocessor.Limits {
	return contentprocessor.Limits{
		SourceBytes: policy.SourceBytes, OutputBytes: policy.DecodedBytes, ExpandedBytes: policy.ExpandedBytes,
		Representations: policy.Representations, TableCells: policy.TableCells, Pages: policy.PDFPages,
		Pixels: policy.ImagePixels, WallTime: policy.ProcessorWallTime, MemoryBytes: policy.ProcessorMemory,
	}
}

func (policy Policy) AllowsProcessing(sensitivity artifactregistry.Sensitivity) bool {
	switch policy.Disclosure {
	case DisclosureClassifiedOnly:
		return sensitivity == artifactregistry.SensitivityPublic || sensitivity == artifactregistry.SensitivityInternal
	case DisclosureAllowSensitive:
		return sensitivity == artifactregistry.SensitivityPublic || sensitivity == artifactregistry.SensitivityInternal || sensitivity == artifactregistry.SensitivitySensitive
	case DisclosureAllowAll:
		return true
	default:
		return false
	}
}
