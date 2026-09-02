// Package pointfinalization validates an immutable implementation-point
// candidate and turns an accepted candidate into one owned Git commit.
package pointfinalization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"darkstar/src/core/attemptexecution"
	executorport "darkstar/src/ports/executor"
	"darkstar/src/ports/repository"
)

var (
	ErrInvalidRequest   = errors.New("invalid point finalization request")
	ErrInvalidCandidate = errors.New("invalid validated point candidate")
	ErrInvalidDecision  = errors.New("invalid point decision")
)

// Repository owns candidate capture and the Git compare-and-swap commit.
type Repository interface {
	CaptureCandidate(context.Context, repository.CaptureCandidateRequest) (repository.Candidate, error)
	CommitCandidate(context.Context, repository.CommitCandidateRequest) (repository.PointCommit, error)
}

// ValidationProfiles resolves the exact configured validation profile frozen by
// the point completion contract.
type ValidationProfiles interface {
	Resolve(context.Context, string) (attemptexecution.OutputValidator, error)
}

type ValidationProfilesFunc func(context.Context, string) (attemptexecution.OutputValidator, error)

func (resolve ValidationProfilesFunc) Resolve(ctx context.Context, profile string) (attemptexecution.OutputValidator, error) {
	return resolve(ctx, profile)
}

// Service is the single success boundary between a dirty point worktree and its
// owned commit. Validation and commit use the same immutable Candidate tree.
type Service struct {
	repository Repository
	profiles   ValidationProfiles
	seal       *validationSeal
}

type validationSeal struct{ marker byte }

func New(repo Repository, profiles ValidationProfiles) (*Service, error) {
	if repo == nil || profiles == nil {
		return nil, errors.New("point finalization requires repository and validation profiles")
	}
	return &Service{repository: repo, profiles: profiles, seal: &validationSeal{marker: 1}}, nil
}

// ValidationRequest selects the candidate and configured profile. AttemptID is
// the evidence owner and must identify the already completed provider attempt.
type ValidationRequest struct {
	AttemptID string
	Profile   string
	Candidate repository.CaptureCandidateRequest
}

// ValidatedCandidate can only be produced by Service.Validate. Its unexported
// state prevents a caller from constructing "validated" with unrelated evidence.
type ValidatedCandidate struct {
	candidate repository.Candidate
	evidence  []executorport.Evidence
	digest    string
	profile   string
	seal      *validationSeal
}

func (candidate ValidatedCandidate) Candidate() repository.Candidate {
	return cloneCandidate(candidate.candidate)
}

func (candidate ValidatedCandidate) Evidence() []executorport.Evidence {
	return append([]executorport.Evidence(nil), candidate.evidence...)
}

func (candidate ValidatedCandidate) Digest() string  { return candidate.digest }
func (candidate ValidatedCandidate) Profile() string { return candidate.profile }

// Validate freezes the worktree candidate first, then runs the configured
// deterministic validator against that exact tree description and workspace.
func (service *Service) Validate(ctx context.Context, request ValidationRequest) (ValidatedCandidate, error) {
	if service == nil || service.repository == nil || service.profiles == nil || service.seal == nil {
		return ValidatedCandidate{}, errors.New("point finalization service is not configured")
	}
	if invalidText(request.AttemptID) || invalidText(request.Profile) {
		return ValidatedCandidate{}, fmt.Errorf("%w: attempt ID and validation profile are required and must be trimmed", ErrInvalidRequest)
	}
	candidate, err := service.repository.CaptureCandidate(ctx, request.Candidate)
	if err != nil {
		return ValidatedCandidate{}, err
	}
	payload, err := json.Marshal(struct {
		ParentSHA string   `json:"parentSha"`
		TreeSHA   string   `json:"treeSha"`
		Manifest  []string `json:"manifest"`
	}{ParentSHA: candidate.ParentSHA, TreeSHA: candidate.TreeSHA, Manifest: candidate.Manifest})
	if err != nil {
		return ValidatedCandidate{}, fmt.Errorf("encode point candidate: %w", err)
	}
	digest := hash(payload)
	validator, err := service.profiles.Resolve(ctx, request.Profile)
	if err != nil {
		return ValidatedCandidate{}, fmt.Errorf("resolve validation profile %q: %w", request.Profile, err)
	}
	if validator == nil {
		return ValidatedCandidate{}, fmt.Errorf("resolve validation profile %q: no validator", request.Profile)
	}
	validation, err := validator.Validate(ctx, attemptexecution.ValidationRequest{
		AttemptID: request.AttemptID,
		Context:   attemptexecution.ContextSnapshot{Digest: digest, Inputs: append(json.RawMessage(nil), payload...), PolicyDigest: hash([]byte(request.Profile))},
		Workspace: candidate.WorktreePath,
		Result:    executorport.Result{CandidateOutput: append(json.RawMessage(nil), payload...), RecoveryRef: "point-candidate:" + digest},
	})
	if err != nil {
		return ValidatedCandidate{}, fmt.Errorf("validate point candidate: %w", err)
	}
	if len(validation.Evidence) == 0 {
		return ValidatedCandidate{}, fmt.Errorf("%w: successful validation requires evidence", ErrInvalidCandidate)
	}
	for index, value := range validation.Evidence {
		if invalidText(value.Kind) || invalidText(value.Ref) || invalidText(value.Digest) {
			return ValidatedCandidate{}, fmt.Errorf("%w: validation evidence %d is incomplete", ErrInvalidCandidate, index+1)
		}
	}
	return ValidatedCandidate{
		candidate: cloneCandidate(candidate), evidence: append([]executorport.Evidence(nil), validation.Evidence...),
		digest: digest, profile: request.Profile, seal: service.seal,
	}, nil
}

// Decision is a closed choice. Rejected candidates are preserved as evidence
// but can never fall through to the repository commit operation.
type Decision interface{ isDecision() }

type Accept struct{}

func (Accept) isDecision() {}

type Reject struct{ Reason string }

func (Reject) isDecision() {}

// FinalizeRequest applies the checkpoint decision to one validated candidate.
type FinalizeRequest struct {
	Validated   ValidatedCandidate
	Decision    Decision
	OperationID string
	Owner       repository.Ownership
	Point       repository.PointRevision
	Subject     string
}

// Outcome is closed so committed and rejected results cannot be represented at
// the same time.
type Outcome interface{ isOutcome() }

type Committed struct {
	Commit   repository.PointCommit
	Evidence []executorport.Evidence
}

func (Committed) isOutcome() {}

type Rejected struct {
	Candidate repository.Candidate
	Evidence  []executorport.Evidence
	Reason    string
}

func (Rejected) isOutcome() {}

// Finalize commits only an explicit acceptance. Reject returns a distinct
// outcome and performs no repository mutation.
func (service *Service) Finalize(ctx context.Context, request FinalizeRequest) (Outcome, error) {
	if service == nil || service.repository == nil || service.seal == nil {
		return nil, errors.New("point finalization service is not configured")
	}
	validated := request.Validated
	if validated.seal != service.seal || validated.digest == "" || validated.profile == "" || len(validated.evidence) == 0 {
		return nil, ErrInvalidCandidate
	}
	switch decision := request.Decision.(type) {
	case Accept:
		commit, err := service.repository.CommitCandidate(ctx, repository.CommitCandidateRequest{
			Candidate: cloneCandidate(validated.candidate), OperationID: request.OperationID, Owner: request.Owner,
			Point: request.Point, Subject: request.Subject,
		})
		if err != nil {
			return nil, err
		}
		return Committed{Commit: commit, Evidence: validated.Evidence()}, nil
	case Reject:
		if invalidText(decision.Reason) {
			return nil, fmt.Errorf("%w: rejection reason is required and must be trimmed", ErrInvalidDecision)
		}
		return Rejected{Candidate: validated.Candidate(), Evidence: validated.Evidence(), Reason: decision.Reason}, nil
	default:
		return nil, ErrInvalidDecision
	}
}

func cloneCandidate(candidate repository.Candidate) repository.Candidate {
	candidate.Manifest = append([]string(nil), candidate.Manifest...)
	return candidate
}

func invalidText(value string) bool {
	return strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value
}

func hash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
