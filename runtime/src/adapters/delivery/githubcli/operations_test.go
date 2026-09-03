package githubcli

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

const (
	oldCommit = "1111111111111111111111111111111111111111"
	newCommit = "2222222222222222222222222222222222222222"
)

var observedAt = time.Date(2026, 9, 3, 7, 0, 0, 0, time.UTC)

func TestProbeHealthResolvesAccountRepositoryBaseAndPushPermission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stdout: []byte("octocat\n")},
		commandResponse{stdout: []byte(`{"full_name":"Darkstar/Runtime","default_branch":"main","html_url":"https://github.com/Darkstar/Runtime","permissions":{"push":true}}`)},
	)
	adapter := operationAdapter(t, runner)
	observation, err := adapter.ProbeHealth(context.Background(), delivery.HealthRequest{
		LocalRepository: root, RemoteName: "origin", Account: "OctoCat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := observation.Outcome.(delivery.HealthReady); !ok {
		t.Fatalf("outcome = %T, want HealthReady", observation.Outcome)
	}
	if observation.Account != "octocat" || observation.Repository.Owner != "Darkstar" || observation.BaseBranch.Name != "main" || observation.BaseBranch.Repository != observation.Repository {
		t.Fatalf("observation = %#v", observation)
	}
	if observation.ObservedAt != observedAt || observation.EvidenceRef != "https://github.com/Darkstar/Runtime" {
		t.Fatalf("evidence = %#v", observation)
	}
	want := [][]string{
		{"-C", filepath.Clean(root), "remote", "get-url", "origin"},
		{"auth", "status", "--hostname", "github.com", "--active"},
		{"api", "--hostname", "github.com", "user", "--jq", ".login"},
		{"api", "--hostname", "github.com", "repos/darkstar/runtime", "--method", "GET"},
	}
	assertCallArguments(t, runner.calls, want)
}

func TestProbeHealthReturnsActionableClosedFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		account   string
		responses []commandResponse
		want      any
	}{
		{name: "authentication", responses: []commandResponse{{stdout: []byte("git@github.com:darkstar/runtime.git\n")}, {err: errors.New("exit")}}, want: delivery.HealthUnauthenticated{}},
		{name: "account mismatch", account: "expected", responses: []commandResponse{{stdout: []byte("git@github.com:darkstar/runtime.git\n")}, {}, {stdout: []byte("other\n")}}, want: delivery.HealthDegraded{}},
		{name: "read only", responses: []commandResponse{{stdout: []byte("git@github.com:darkstar/runtime.git\n")}, {}, {stdout: []byte("octocat\n")}, {stdout: []byte(`{"full_name":"darkstar/runtime","default_branch":"main","permissions":{"push":false}}`)}}, want: delivery.HealthReadOnly{}},
		{name: "repository unavailable", responses: []commandResponse{{stdout: []byte("git@github.com:darkstar/runtime.git\n")}, {}, {stdout: []byte("octocat\n")}, {err: errors.New("exit")}}, want: delivery.HealthUnavailable{}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := scriptedRunner(t, test.responses...)
			adapter := operationAdapter(t, runner)
			observation, err := adapter.ProbeHealth(context.Background(), delivery.HealthRequest{LocalRepository: t.TempDir(), RemoteName: "origin", Account: test.account})
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(observation.Outcome) != reflect.TypeOf(test.want) {
				t.Fatalf("outcome = %T, want %T", observation.Outcome, test.want)
			}
			switch outcome := observation.Outcome.(type) {
			case delivery.HealthUnauthenticated:
				if outcome.Reason == "" {
					t.Fatal("missing action")
				}
			case delivery.HealthDegraded:
				if outcome.Reason == "" {
					t.Fatal("missing action")
				}
			case delivery.HealthReadOnly:
				if outcome.Reason == "" {
					t.Fatal("missing action")
				}
			case delivery.HealthUnavailable:
				if outcome.Reason == "" {
					t.Fatal("missing action")
				}
			}
		})
	}
}

func TestObserveBranchReportsFoundAndMissingWithoutMutation(t *testing.T) {
	t.Parallel()
	t.Run("found", func(t *testing.T) {
		runner := scriptedRunner(t, commandResponse{stdout: []byte(newCommit + "\n")})
		adapter := operationAdapter(t, runner)
		observation, err := adapter.ObserveBranch(context.Background(), delivery.ObserveBranchRequest{Branch: testBranch()})
		if err != nil {
			t.Fatal(err)
		}
		found, ok := observation.Outcome.(delivery.BranchFound)
		if !ok || found.CommitSHA != newCommit {
			t.Fatalf("outcome = %#v", observation.Outcome)
		}
		assertNoMutationArguments(t, runner.calls)
	})
	t.Run("missing", func(t *testing.T) {
		runner := scriptedRunner(t, commandResponse{stderr: []byte("HTTP 404: Not Found"), err: errors.New("exit")})
		adapter := operationAdapter(t, runner)
		observation, err := adapter.ObserveBranch(context.Background(), delivery.ObserveBranchRequest{Branch: testBranch()})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := observation.Outcome.(delivery.BranchMissing); !ok {
			t.Fatalf("outcome = %T", observation.Outcome)
		}
		assertNoMutationArguments(t, runner.calls)
	})
}

func TestPublishBranchCreatesWithExactMissingLeaseAndReturnsOwnership(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("exit")},
		commandResponse{},
	)
	adapter := operationAdapter(t, runner)
	publication, err := adapter.PublishBranch(context.Background(), publicationRequest(root, delivery.RemoteBranchMissing{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := publication.Outcome.(delivery.BranchPublished); !ok {
		t.Fatalf("outcome = %T", publication.Outcome)
	}
	if publication.Ownership.Owner != testOwner() || publication.Ownership.EstablishedByOperationID != "publish-1" {
		t.Fatalf("ownership = %#v", publication.Ownership)
	}
	wantPush := []string{"-C", filepath.Clean(root), "push", "--porcelain", "--force-with-lease=refs/heads/codex/dar-89:", "origin", newCommit + ":refs/heads/codex/dar-89"}
	if !reflect.DeepEqual(runner.calls[3].arguments, wantPush) {
		t.Fatalf("push = %#v", runner.calls[3].arguments)
	}
	assertNoUnsafeForce(t, runner.calls[3].arguments)
}

func TestPublishBranchReconcilesRetryWhenTargetCommitIsAlreadyPublished(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("https://github.com/darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stdout: []byte(newCommit + "\n")},
	)
	adapter := operationAdapter(t, runner)
	publication, err := adapter.PublishBranch(context.Background(), publicationRequest(root, delivery.RemoteBranchMissing{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := publication.Outcome.(delivery.BranchAlreadyPublished); !ok {
		t.Fatalf("outcome = %T", publication.Outcome)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d, want no second push", len(runner.calls))
	}
}

func TestPublishBranchHonorsIncrementalPointTiming(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("exit")},
		commandResponse{},
	)
	adapter := operationAdapter(t, runner)
	request := publicationRequest(root, delivery.RemoteBranchMissing{})
	request.Timing = delivery.PublishAfterPointAcceptance{AcceptedCommitSHA: newCommit, PointID: "point-7", PointRevision: 2}
	publication, err := adapter.PublishBranch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if publication.CommitSHA != newCommit {
		t.Fatalf("commit = %q", publication.CommitSHA)
	}
	request.Timing = delivery.PublishAfterPointAcceptance{AcceptedCommitSHA: newCommit}
	before := len(runner.calls)
	_, err = adapter.PublishBranch(context.Background(), request)
	assertFailureCode(t, err, ports.FailureInvalidRequest)
	if len(runner.calls) != before {
		t.Fatal("invalid incremental timing reached the command boundary")
	}
}

func TestPublishBranchAdvancesOnlyOwnedAncestorWithExactLease(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ownership := delivery.BranchOwnershipEvidence{Owner: testOwner(), EstablishedByOperationID: "publish-0"}
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stdout: []byte(oldCommit + "\n")},
		commandResponse{},
		commandResponse{},
	)
	adapter := operationAdapter(t, runner)
	publication, err := adapter.PublishBranch(context.Background(), publicationRequest(root, delivery.OwnedRemoteBranchAt{CommitSHA: oldCommit, Ownership: ownership}))
	if err != nil {
		t.Fatal(err)
	}
	if publication.Ownership != ownership {
		t.Fatalf("ownership = %#v", publication.Ownership)
	}
	wantLease := "--force-with-lease=refs/heads/codex/dar-89:" + oldCommit
	if runner.calls[4].arguments[4] != wantLease {
		t.Fatalf("lease = %q", runner.calls[4].arguments[4])
	}
	assertNoUnsafeForce(t, runner.calls[4].arguments)
}

func TestPublishBranchBlocksDivergenceAndNonFastForward(t *testing.T) {
	t.Parallel()
	ownership := delivery.BranchOwnershipEvidence{Owner: testOwner(), EstablishedByOperationID: "publish-0"}
	expectation := delivery.OwnedRemoteBranchAt{CommitSHA: oldCommit, Ownership: ownership}
	tests := []struct {
		name      string
		responses []commandResponse
	}{
		{name: "remote divergence", responses: []commandResponse{{stdout: []byte("git@github.com:darkstar/runtime.git\n")}, {}, {stdout: []byte("3333333333333333333333333333333333333333\n")}}},
		{name: "non fast forward", responses: []commandResponse{{stdout: []byte("git@github.com:darkstar/runtime.git\n")}, {}, {stdout: []byte(oldCommit + "\n")}, {err: errors.New("not ancestor")}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := scriptedRunner(t, test.responses...)
			adapter := operationAdapter(t, runner)
			_, err := adapter.PublishBranch(context.Background(), publicationRequest(t.TempDir(), expectation))
			assertFailureCode(t, err, ports.FailureConflict)
			for _, call := range runner.calls {
				if len(call.arguments) > 2 && call.arguments[2] == "push" {
					t.Fatalf("unsafe push attempted: %#v", call.arguments)
				}
			}
		})
	}
}

func TestPublishBranchReconcilesSuccessfulPushAfterCommandFailure(t *testing.T) {
	t.Parallel()
	runner := scriptedRunner(t,
		commandResponse{stdout: []byte("git@github.com:darkstar/runtime.git\n")},
		commandResponse{},
		commandResponse{stderr: []byte("HTTP 404"), err: errors.New("exit")},
		commandResponse{err: errors.New("connection lost")},
		commandResponse{stdout: []byte(newCommit + "\n")},
	)
	adapter := operationAdapter(t, runner)
	publication, err := adapter.PublishBranch(context.Background(), publicationRequest(t.TempDir(), delivery.RemoteBranchMissing{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := publication.Outcome.(delivery.BranchAlreadyPublished); !ok {
		t.Fatalf("outcome = %T", publication.Outcome)
	}
}

func operationAdapter(t *testing.T, runner *fakeRunner) *Adapter {
	t.Helper()
	adapter, err := New(Options{Runner: runner, Now: func() time.Time { return observedAt }})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func scriptedRunner(t *testing.T, responses ...commandResponse) *fakeRunner {
	t.Helper()
	root := t.TempDir()
	return &fakeRunner{resolvedByName: map[string]string{"gh": filepath.Join(root, "gh.exe"), "git": filepath.Join(root, "git.exe")}, responses: append([]commandResponse(nil), responses...)}
}

func testRepository() delivery.Repository {
	return delivery.Repository{Provider: Provider, Host: "github.com", Owner: "darkstar", Name: "runtime"}
}

func testBranch() delivery.BranchRef {
	return delivery.BranchRef{Repository: testRepository(), Name: "codex/dar-89"}
}

func testOwner() delivery.BranchOwner {
	return delivery.BranchOwner{DeliveryLineID: "delivery-1", WorkItemID: "work-1"}
}

func publicationRequest(root string, expected delivery.RemoteBranchExpectation) delivery.PublishBranchRequest {
	return delivery.PublishBranchRequest{
		OperationID: "publish-1", LocalRepository: root, RemoteName: "origin", Owner: testOwner(),
		Timing:      delivery.PublishAfterFinalValidation{ValidatedCommitSHA: newCommit},
		Destination: testBranch(), ExpectedRemote: expected,
	}
}

func assertCallArguments(t *testing.T, calls []commandCall, want [][]string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("calls = %d, want %d", len(calls), len(want))
	}
	for index := range want {
		if !reflect.DeepEqual(calls[index].arguments, want[index]) {
			t.Fatalf("call %d = %#v, want %#v", index, calls[index].arguments, want[index])
		}
	}
}

func assertNoMutationArguments(t *testing.T, calls []commandCall) {
	t.Helper()
	for _, call := range calls {
		joined := strings.Join(call.arguments, " ")
		if strings.Contains(joined, " push ") || strings.Contains(joined, "--method POST") || strings.Contains(joined, "--method PATCH") || strings.Contains(joined, "--method DELETE") {
			t.Fatalf("mutation command = %#v", call.arguments)
		}
	}
}

func assertNoUnsafeForce(t *testing.T, arguments []string) {
	t.Helper()
	for _, argument := range arguments {
		if argument == "--force" || strings.HasPrefix(argument, "+") {
			t.Fatalf("unsafe force argument = %q", argument)
		}
	}
}
