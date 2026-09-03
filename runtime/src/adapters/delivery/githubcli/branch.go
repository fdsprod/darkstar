package githubcli

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

func (adapter *Adapter) ObserveBranch(ctx context.Context, request delivery.ObserveBranchRequest) (delivery.BranchObservation, error) {
	if adapter == nil || adapter.runner == nil || adapter.now == nil {
		return delivery.BranchObservation{}, failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	branch := request.Branch
	branch.Repository = normalizedRepository(branch.Repository)
	if err := validateBranch(branch); err != nil {
		return delivery.BranchObservation{}, err
	}
	arguments := []string{"api", "--hostname", branch.Repository.Host, repositoryAPIPath(branch.Repository) + "/git/ref/heads/" + url.PathEscape(branch.Name), "--method", "GET", "--jq", ".object.sha"}
	result := adapter.execute(ctx, adapter.executable, arguments, nil)
	observation := delivery.BranchObservation{Branch: branch, ObservedAt: adapter.now().UTC()}
	if result.err != nil {
		if commandReportsNotFound(result.stderr) {
			observation.Outcome = delivery.BranchMissing{}
			observation.EvidenceRef = branchEvidenceRef(branch, "missing")
			return observation, nil
		}
		return delivery.BranchObservation{}, normalizeCommandFailure(ctx, result.err)
	}
	commit := strings.TrimSpace(string(result.stdout))
	if !commitPattern.MatchString(commit) {
		return delivery.BranchObservation{}, failure(ports.FailureProtocolDrift, "GitHub branch response did not contain a commit SHA", false)
	}
	commit = strings.ToLower(commit)
	observation.Outcome = delivery.BranchFound{CommitSHA: commit}
	observation.EvidenceRef = branchEvidenceRef(branch, commit)
	return observation, nil
}

func (adapter *Adapter) PublishBranch(ctx context.Context, request delivery.PublishBranchRequest) (delivery.BranchPublication, error) {
	commit, err := validatePublicationRequest(request)
	if err != nil {
		return delivery.BranchPublication{}, err
	}
	request.Destination.Repository = normalizedRepository(request.Destination.Repository)
	remoteResult := adapter.execute(ctx, adapter.gitExecutable, []string{"-C", filepath.Clean(request.LocalRepository), "remote", "get-url", request.RemoteName}, nil)
	if remoteResult.err != nil {
		return delivery.BranchPublication{}, normalizeCommandFailure(ctx, remoteResult.err)
	}
	remoteRepository, err := repositoryFromRemoteURL(strings.TrimSpace(string(remoteResult.stdout)))
	if err != nil {
		return delivery.BranchPublication{}, err
	}
	if !sameRepository(remoteRepository, request.Destination.Repository) {
		return delivery.BranchPublication{}, failure(ports.FailureConflict, "configured Git remote does not match the publication destination", false)
	}
	if _, err := adapter.runGit(ctx, []string{"-C", filepath.Clean(request.LocalRepository), "cat-file", "-e", commit + "^{commit}"}); err != nil {
		return delivery.BranchPublication{}, failure(ports.FailureNotFound, "publication commit is not present in the local repository", false)
	}

	observation, err := adapter.ObserveBranch(ctx, delivery.ObserveBranchRequest{Branch: request.Destination})
	if err != nil {
		return delivery.BranchPublication{}, err
	}
	ownership, already, prior, err := reconcilePublicationPrecondition(request, observation, commit)
	if err != nil {
		return delivery.BranchPublication{}, err
	}
	if already {
		return adapter.publication(request, commit, ownership, delivery.BranchAlreadyPublished{}), nil
	}
	if prior != "" {
		ancestry := adapter.execute(ctx, adapter.gitExecutable, []string{"-C", filepath.Clean(request.LocalRepository), "merge-base", "--is-ancestor", prior, commit}, nil)
		if ancestry.err != nil {
			if ctx.Err() != nil {
				return delivery.BranchPublication{}, normalizeCommandFailure(ctx, ancestry.err)
			}
			return delivery.BranchPublication{}, failure(ports.FailureConflict, "publication would not fast-forward the owned remote branch", false)
		}
	}
	refspec := commit + ":refs/heads/" + request.Destination.Name
	lease := "--force-with-lease=refs/heads/" + request.Destination.Name + ":" + prior
	push := adapter.execute(ctx, adapter.gitExecutable, []string{"-C", filepath.Clean(request.LocalRepository), "push", "--porcelain", lease, request.RemoteName, refspec}, nil)
	if push.err == nil {
		return adapter.publication(request, commit, ownership, delivery.BranchPublished{}), nil
	}
	if ctx.Err() != nil {
		return delivery.BranchPublication{}, normalizeCommandFailure(ctx, push.err)
	}
	reconciled, observeErr := adapter.ObserveBranch(ctx, delivery.ObserveBranchRequest{Branch: request.Destination})
	if observeErr != nil {
		return delivery.BranchPublication{}, failure(ports.FailureUncertain, "branch push failed and remote state could not be proven", false)
	}
	if found, ok := reconciled.Outcome.(delivery.BranchFound); ok && strings.EqualFold(found.CommitSHA, commit) {
		return adapter.publication(request, commit, ownership, delivery.BranchAlreadyPublished{}), nil
	}
	if remoteMatchesPrior(reconciled.Outcome, prior) {
		return delivery.BranchPublication{}, failure(ports.FailureUnavailable, "branch push did not complete; retry the same publication operation", true)
	}
	return delivery.BranchPublication{}, failure(ports.FailureConflict, "remote branch changed while publication was in progress", false)
}

func validatePublicationRequest(request delivery.PublishBranchRequest) (string, error) {
	if strings.TrimSpace(request.OperationID) == "" || request.OperationID != strings.TrimSpace(request.OperationID) {
		return "", failure(ports.FailureInvalidRequest, "publication operation ID is required and must be trimmed", false)
	}
	if !filepath.IsAbs(request.LocalRepository) || !validRepositoryComponent(request.RemoteName) {
		return "", failure(ports.FailureInvalidRequest, "publication requires an absolute local repository and valid remote name", false)
	}
	if strings.TrimSpace(request.Owner.DeliveryLineID) == "" || strings.TrimSpace(request.Owner.WorkItemID) == "" {
		return "", failure(ports.FailureInvalidRequest, "branch owner requires delivery-line and work-item identities", false)
	}
	if err := validateBranch(request.Destination); err != nil {
		return "", err
	}
	var commit string
	switch timing := request.Timing.(type) {
	case delivery.PublishAfterFinalValidation:
		commit = timing.ValidatedCommitSHA
	case delivery.PublishAfterPointAcceptance:
		if strings.TrimSpace(timing.PointID) == "" || timing.PointRevision == 0 {
			return "", failure(ports.FailureInvalidRequest, "incremental publication requires a point identity and revision", false)
		}
		commit = timing.AcceptedCommitSHA
	default:
		return "", failure(ports.FailureInvalidRequest, "publication timing policy is required", false)
	}
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !commitPattern.MatchString(commit) {
		return "", failure(ports.FailureInvalidRequest, "publication timing requires an exact commit SHA", false)
	}
	return commit, nil
}

func reconcilePublicationPrecondition(request delivery.PublishBranchRequest, observation delivery.BranchObservation, commit string) (delivery.BranchOwnershipEvidence, bool, string, error) {
	created := delivery.BranchOwnershipEvidence{Owner: request.Owner, EstablishedByOperationID: request.OperationID}
	switch expected := request.ExpectedRemote.(type) {
	case delivery.RemoteBranchMissing:
		switch actual := observation.Outcome.(type) {
		case delivery.BranchMissing:
			return created, false, "", nil
		case delivery.BranchFound:
			if strings.EqualFold(actual.CommitSHA, commit) {
				return created, true, "", nil
			}
			return delivery.BranchOwnershipEvidence{}, false, "", failure(ports.FailureConflict, "remote branch exists without matching publication intent", false)
		}
	case delivery.OwnedRemoteBranchAt:
		if expected.Ownership.Owner != request.Owner || strings.TrimSpace(expected.Ownership.EstablishedByOperationID) == "" || !commitPattern.MatchString(strings.TrimSpace(expected.CommitSHA)) {
			return delivery.BranchOwnershipEvidence{}, false, "", failure(ports.FailureInvalidRequest, "existing remote branch requires matching ownership evidence and commit", false)
		}
		actual, found := observation.Outcome.(delivery.BranchFound)
		if !found {
			return delivery.BranchOwnershipEvidence{}, false, "", failure(ports.FailureConflict, "owned remote branch is unexpectedly missing", false)
		}
		if strings.EqualFold(actual.CommitSHA, commit) {
			return expected.Ownership, true, "", nil
		}
		if !strings.EqualFold(actual.CommitSHA, expected.CommitSHA) {
			return delivery.BranchOwnershipEvidence{}, false, "", failure(ports.FailureConflict, "remote branch tip does not match the owned expected commit", false)
		}
		return expected.Ownership, false, strings.ToLower(expected.CommitSHA), nil
	default:
		return delivery.BranchOwnershipEvidence{}, false, "", failure(ports.FailureInvalidRequest, "remote branch expectation is required", false)
	}
	return delivery.BranchOwnershipEvidence{}, false, "", failure(ports.FailureProtocolDrift, "GitHub branch observation was invalid", false)
}

func (adapter *Adapter) publication(request delivery.PublishBranchRequest, commit string, ownership delivery.BranchOwnershipEvidence, outcome delivery.BranchPublicationOutcome) delivery.BranchPublication {
	return delivery.BranchPublication{Branch: request.Destination, CommitSHA: commit, Outcome: outcome, Ownership: ownership, ObservedAt: adapter.now().UTC(), EvidenceRef: branchEvidenceRef(request.Destination, commit) + "?operation=" + url.QueryEscape(request.OperationID)}
}

func validateBranch(branch delivery.BranchRef) error {
	if err := validateRepository(branch.Repository); err != nil {
		return err
	}
	if !validRefName(branch.Name) || strings.HasPrefix(branch.Name, "/") || strings.HasSuffix(branch.Name, "/") || strings.Contains(branch.Name, "//") {
		return failure(ports.FailureInvalidRequest, "GitHub branch name is invalid", false)
	}
	return nil
}

func sameRepository(left, right delivery.Repository) bool {
	left, right = normalizedRepository(left), normalizedRepository(right)
	return strings.EqualFold(left.Host, right.Host) && strings.EqualFold(left.Owner, right.Owner) && strings.EqualFold(left.Name, right.Name)
}

func branchEvidenceRef(branch delivery.BranchRef, observation string) string {
	repository := normalizedRepository(branch.Repository)
	return fmt.Sprintf("github://%s/%s/%s/refs/heads/%s@%s", repository.Host, repository.Owner, repository.Name, url.PathEscape(branch.Name), observation)
}

func commandReportsNotFound(stderr []byte) bool {
	value := strings.ToLower(string(stderr))
	return strings.Contains(value, "http 404") || strings.Contains(value, "status 404") || strings.Contains(value, "not found (http 404)")
}

func remoteMatchesPrior(outcome delivery.BranchOutcome, prior string) bool {
	if prior == "" {
		_, missing := outcome.(delivery.BranchMissing)
		return missing
	}
	found, ok := outcome.(delivery.BranchFound)
	return ok && strings.EqualFold(found.CommitSHA, prior)
}
