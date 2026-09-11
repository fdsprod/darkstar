package git

import (
	"context"
	"darkstar/src/ports/repository"
	"strings"
)

// CommitWorkflowCandidate commits the connected snapshot without a point policy.
func (manager *Manager) CommitWorkflowCandidate(ctx context.Context, request repository.WorkflowCommitRequest) (repository.PointCommit, error) {
	c := request.Candidate
	if err := validateCandidateLocation(c.Repository.Root, c.WorktreePath, c.BranchName, c.ParentSHA); err != nil {
		return repository.PointCommit{}, err
	}
	if err := validateMutation(c.Repository.Root, c.WorktreePath, request.OperationID, request.Owner); err != nil {
		return repository.PointCommit{}, err
	}
	if c.Repository.CommonGitDir == "" || c.TreeSHA == "" || len(c.Manifest) == 0 {
		return repository.PointCommit{}, invalid("commit requires a captured candidate")
	}
	for _, value := range []string{request.Subject, request.OperationID, request.Owner.WorkItemID, request.Owner.DeliveryLineID} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return repository.PointCommit{}, invalid("commit identity and subject must be nonempty single lines")
		}
	}
	if strings.ContainsRune(request.Body, '\x00') {
		return repository.PointCommit{}, invalid("commit body contains NUL")
	}
	message := request.Subject + "\n\n" + strings.TrimSpace(request.Body) + "\n\nDarkstar-Workflow: " + request.Owner.DeliveryLineID + "\nDarkstar-Work-Item: " + request.Owner.WorkItemID + "\nDarkstar-Operation: " + request.OperationID + "\n"
	return manager.commitCandidateMessage(ctx, repository.CommitCandidateRequest{Candidate: c, OperationID: request.OperationID, Owner: request.Owner, Subject: request.Subject}, message)
}
