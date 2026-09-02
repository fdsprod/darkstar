package pointfinalization

import (
	"context"
	"errors"
	"testing"

	"darkstar/src/core/attemptexecution"
	executorport "darkstar/src/ports/executor"
	"darkstar/src/ports/repository"
)

type fakeRepository struct {
	candidate  repository.Candidate
	captureErr error
	commit     repository.PointCommit
	commitErr  error
	captures   int
	commits    int
	lastCommit repository.CommitCandidateRequest
}

func (fake *fakeRepository) CaptureCandidate(context.Context, repository.CaptureCandidateRequest) (repository.Candidate, error) {
	fake.captures++
	return cloneCandidate(fake.candidate), fake.captureErr
}

func (fake *fakeRepository) CommitCandidate(_ context.Context, request repository.CommitCandidateRequest) (repository.PointCommit, error) {
	fake.commits++
	fake.lastCommit = request
	return fake.commit, fake.commitErr
}

type fakeValidator struct {
	request attemptexecution.ValidationRequest
	result  attemptexecution.ValidationResult
	err     error
}

func (fake *fakeValidator) Validate(_ context.Context, request attemptexecution.ValidationRequest) (attemptexecution.ValidationResult, error) {
	fake.request = request
	return fake.result, fake.err
}

func baseCandidate() repository.Candidate {
	return repository.Candidate{
		Repository:   repository.Identity{Root: "C:/repo", CommonGitDir: "C:/repo/.git"},
		WorktreePath: "C:/worktree", BranchName: "darkstar/work", ParentSHA: "parent", TreeSHA: "tree",
		Manifest: []string{"a.txt", "b.txt"},
	}
}

func baseEvidence() []executorport.Evidence {
	return []executorport.Evidence{{Kind: "command-validation", Ref: "evidence.json", Digest: "digest"}}
}

func newTestService(t *testing.T, repo *fakeRepository, validator *fakeValidator) *Service {
	t.Helper()
	service, err := New(repo, ValidationProfilesFunc(func(_ context.Context, profile string) (attemptexecution.OutputValidator, error) {
		if profile != "default" {
			t.Fatalf("profile = %q, want default", profile)
		}
		return validator, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func validateCandidate(t *testing.T, service *Service) ValidatedCandidate {
	t.Helper()
	validated, err := service.Validate(context.Background(), ValidationRequest{
		AttemptID: "attempt_1", Profile: "default",
		Candidate: repository.CaptureCandidateRequest{RepositoryPath: "C:/repo", WorktreePath: "C:/worktree", BranchName: "darkstar/work", ExpectedParentSHA: "parent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return validated
}

func TestAcceptedValidatedCandidateCreatesOwnedCommit(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{candidate: baseCandidate(), commit: repository.PointCommit{CommitSHA: "commit", ParentSHA: "parent", TreeSHA: "tree"}}
	validator := &fakeValidator{result: attemptexecution.ValidationResult{Evidence: baseEvidence()}}
	service := newTestService(t, repo, validator)
	validated := validateCandidate(t, service)

	outcome, err := service.Finalize(context.Background(), FinalizeRequest{
		Validated: validated, Decision: Accept{}, OperationID: "operation_1",
		Owner:   repository.Ownership{DeliveryLineID: "delivery_1", WorkItemID: "work_1"},
		Point:   repository.PointRevision{RunID: "run_1", StoryID: "story_1", PointID: "point_1", Revision: 3},
		Subject: "feat: finish point",
	})
	if err != nil {
		t.Fatal(err)
	}
	committed, ok := outcome.(Committed)
	if !ok || committed.Commit.CommitSHA != "commit" || len(committed.Evidence) != 1 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if repo.commits != 1 || repo.lastCommit.OperationID != "operation_1" || repo.lastCommit.Point.Revision != 3 {
		t.Fatalf("commit calls = %d, request = %#v", repo.commits, repo.lastCommit)
	}
	if validator.request.Workspace != "C:/worktree" || validator.request.AttemptID != "attempt_1" {
		t.Fatalf("validation request = %#v", validator.request)
	}
	if validated.Digest() == "" || validated.Profile() != "default" {
		t.Fatalf("validated candidate digest/profile = %q/%q", validated.Digest(), validated.Profile())
	}
}

func TestValidationFailureNeverCommits(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{candidate: baseCandidate()}
	validator := &fakeValidator{err: errors.New("tests failed")}
	service := newTestService(t, repo, validator)
	_, err := service.Validate(context.Background(), ValidationRequest{
		AttemptID: "attempt_failed", Profile: "default",
		Candidate: repository.CaptureCandidateRequest{RepositoryPath: "C:/repo", WorktreePath: "C:/worktree", BranchName: "darkstar/work", ExpectedParentSHA: "parent"},
	})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if repo.commits != 0 {
		t.Fatalf("failed validation created %d commits", repo.commits)
	}
}

func TestRejectedCandidateHasDistinctOutcomeAndNeverCommits(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{candidate: baseCandidate()}
	validator := &fakeValidator{result: attemptexecution.ValidationResult{Evidence: baseEvidence()}}
	service := newTestService(t, repo, validator)
	validated := validateCandidate(t, service)

	outcome, err := service.Finalize(context.Background(), FinalizeRequest{Validated: validated, Decision: Reject{Reason: "scope is incorrect"}})
	if err != nil {
		t.Fatal(err)
	}
	rejected, ok := outcome.(Rejected)
	if !ok || rejected.Reason != "scope is incorrect" || rejected.Candidate.TreeSHA != "tree" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if repo.commits != 0 {
		t.Fatalf("rejected candidate created %d commits", repo.commits)
	}
}

func TestFabricatedOrCrossServiceValidationCannotCommit(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{candidate: baseCandidate()}
	validator := &fakeValidator{result: attemptexecution.ValidationResult{Evidence: baseEvidence()}}
	first := newTestService(t, repo, validator)
	second := newTestService(t, repo, validator)
	validated := validateCandidate(t, first)

	for _, value := range []ValidatedCandidate{{}, validated} {
		service := first
		if value.seal != nil {
			service = second
		}
		_, err := service.Finalize(context.Background(), FinalizeRequest{Validated: value, Decision: Accept{}})
		if !errors.Is(err, ErrInvalidCandidate) {
			t.Fatalf("error = %v, want invalid candidate", err)
		}
	}
	if repo.commits != 0 {
		t.Fatalf("invalid candidate created %d commits", repo.commits)
	}
}

func TestSuccessfulValidationRequiresCompleteEvidence(t *testing.T) {
	t.Parallel()
	for _, evidence := range [][]executorport.Evidence{nil, {{Kind: "command", Ref: "", Digest: "digest"}}} {
		repo := &fakeRepository{candidate: baseCandidate()}
		validator := &fakeValidator{result: attemptexecution.ValidationResult{Evidence: evidence}}
		service := newTestService(t, repo, validator)
		_, err := service.Validate(context.Background(), ValidationRequest{
			AttemptID: "attempt_1", Profile: "default",
			Candidate: repository.CaptureCandidateRequest{RepositoryPath: "C:/repo", WorktreePath: "C:/worktree", BranchName: "darkstar/work", ExpectedParentSHA: "parent"},
		})
		if !errors.Is(err, ErrInvalidCandidate) {
			t.Fatalf("evidence %#v error = %v", evidence, err)
		}
	}
}
