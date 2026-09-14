// Package investigationrunner executes one bounded reasoning task. The daemon
// validates durable submissions; final assistant text never completes a unit.
package investigationrunner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/investigation"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/repositorysnapshot"
	"darkstar/src/ports/valueschema"
)

const MaxContentBytes = 1024 * 1024

type EvidenceReader interface {
	repositorysnapshot.Reader
	Verify(context.Context, repositorysnapshot.Evidence) error
}

type Artifacts interface {
	Ingest(context.Context, artifactops.IngestInput, string) (artifactingest.Result, error)
	OriginalContent(context.Context, artifactregistry.VersionRef) (artifactops.Content, error)
}

type Dependencies struct {
	Schema          valueschema.Validator
	Provider        func(context.Context, investigation.ProviderSelection, string, bool) (provider.Provider, error)
	ReleaseProvider func(string)
	ResolveProvider func(context.Context, string) (investigation.ProviderSelection, error)
	Evidence        EvidenceReader
	Artifacts       Artifacts
	ValidateBrief   func(context.Context, string, json.RawMessage) error
	Timeout         time.Duration
}

type Service struct {
	dependencies Dependencies
}

func New(dependencies Dependencies) (*Service, error) {
	if dependencies.Schema == nil || dependencies.Provider == nil || dependencies.ResolveProvider == nil || dependencies.Evidence == nil || dependencies.Artifacts == nil || dependencies.ValidateBrief == nil {
		return nil, errors.New("investigation runner requires provider factory, evidence, artifacts, and brief validation")
	}
	if dependencies.Timeout <= 0 {
		dependencies.Timeout = 15 * time.Minute
	}
	return &Service{dependencies: dependencies}, nil
}

type CodeReference struct {
	RepositoryID string `json:"repositoryId"`
	CommitSHA    string `json:"commitSha"`
	Path         string `json:"path"`
	BlobSHA      string `json:"blobSha"`
	StartLine    int    `json:"startLine"`
	EndLine      int    `json:"endLine"`
}

type RepositoryFindings struct {
	SchemaVersion       int             `json:"schemaVersion"`
	RepositoryID        string          `json:"repositoryId"`
	CommitSHA           string          `json:"commitSha"`
	Quality             string          `json:"quality"`
	Summary             string          `json:"summary"`
	CodeReferences      []CodeReference `json:"codeReferences"`
	AffectedInterfaces  []string        `json:"affectedInterfaces"`
	ReusablePatterns    []string        `json:"reusablePatterns"`
	Constraints         []string        `json:"constraints"`
	Risks               []string        `json:"risks"`
	UnresolvedQuestions []string        `json:"unresolvedQuestions"`
	Limitations         []string        `json:"limitations"`
}

type FindingReference struct {
	RepositoryID string `json:"repositoryId"`
	ArtifactID   string `json:"artifactId"`
	Version      uint64 `json:"version"`
	Digest       string `json:"digest"`
}

type Synthesis struct {
	SchemaVersion               int                         `json:"schemaVersion"`
	Quality                     string                      `json:"quality"`
	EvidenceStatus              string                      `json:"evidenceStatus"`
	Summary                     string                      `json:"summary"`
	Findings                    []FindingReference          `json:"findings"`
	Missing                     []investigation.MissingUnit `json:"missing"`
	CrossRepositoryImplications []string                    `json:"crossRepositoryImplications"`
	Constraints                 []string                    `json:"constraints"`
	Risks                       []string                    `json:"risks"`
	UnresolvedQuestions         []string                    `json:"unresolvedQuestions"`
	Limitations                 []string                    `json:"limitations"`
}
