package githubcli

import (
	"context"
	"errors"
	"testing"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

func contractUnownedBranch(t *testing.T) {
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stdout: []byte(oldCommit + "\n")},
	)
	_, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	assertFailureCode(t, err, ports.FailureConflict)
	assertNoExternalMutation(t, runner.calls)
}

func contractDivergedBranch(t *testing.T) {
	owner := delivery.BranchOwnershipEvidence{Owner: testOwner(), EstablishedByOperationID: "publish-original"}
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stdout: []byte("3333333333333333333333333333333333333333\n")},
	)
	_, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.OwnedRemoteBranchAt{CommitSHA: oldCommit, Ownership: owner}))
	assertFailureCode(t, err, ports.FailureConflict)
	assertNoExternalMutation(t, runner.calls)
}

func contractUnownedPullRequest(t *testing.T) {
	request := finalChangeRequest()
	runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 94, title: request.Title, body: "Human-authored pull request."})})
	_, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	assertFailureCode(t, err, ports.FailureConflict)
	assertNoExternalMutation(t, runner.calls)
}

func contractAmbiguousPullRequest(t *testing.T) {
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t,
		pullSpec{number: 93, title: request.Title, body: body},
		pullSpec{number: 94, title: request.Title, body: body},
	)})
	_, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	assertFailureCode(t, err, ports.FailureConflict)
	assertNoExternalMutation(t, runner.calls)
}

func contractClosedPullRequest(t *testing.T) {
	request := finalChangeRequest()
	body := renderFinalChangeRequestBody(request.Owner, creationContent(request))
	runner := scriptedRunner(t, commandResponse{stdout: pullResponsesJSON(t, pullSpec{number: 94, title: request.Title, body: body, state: "closed"})})
	_, err := operationAdapter(t, runner).CreateChangeRequest(context.Background(), request)
	assertFailureCode(t, err, ports.FailureConflict)
	assertNoExternalMutation(t, runner.calls)
}

func contractRejectedPushUnchanged(t *testing.T) {
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("not found")},
		commandResponse{err: errors.New("push rejected")},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("not found")},
	)
	_, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	assertFailureCode(t, err, ports.FailureUnavailable)
	assertOneSafePush(t, runner.calls)
}

func contractRejectedPushUncertain(t *testing.T) {
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("not found")},
		commandResponse{err: errors.New("push connection lost")},
		commandResponse{err: errors.New("remote observation unavailable")},
	)
	_, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	assertFailureCode(t, err, ports.FailureUncertain)
	assertOneSafePush(t, runner.calls)
}

func contractRejectedPushDiverged(t *testing.T) {
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("not found")},
		commandResponse{err: errors.New("push rejected")},
		commandResponse{stdout: []byte("3333333333333333333333333333333333333333\n")},
	)
	_, err := operationAdapter(t, runner).PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	assertFailureCode(t, err, ports.FailureConflict)
	assertOneSafePush(t, runner.calls)
}
