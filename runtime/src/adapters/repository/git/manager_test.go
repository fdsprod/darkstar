package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/ports"
	"darkstar/src/ports/repository"
)

func TestAttachIsolatesDirtyUserCheckoutAndCleanupPreservesBranch(t *testing.T) {
	t.Parallel()
	fixture := newRepository(t)
	manager := newManager(t)
	base, err := manager.ResolveBase(context.Background(), repository.ResolveBaseRequest{RepositoryPath: fixture.repository, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, "tracked.txt"), []byte("user change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, "untracked.txt"), []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := gitOutput(t, fixture.repository, "status", "--porcelain=v1", "--untracked-files=all")
	worktreePath := filepath.Join(fixture.root, "owned worktrees", "run-1")
	request := repository.AttachRequest{
		RepositoryPath: fixture.repository,
		WorktreePath:   worktreePath,
		OperationID:    "operation_attach_1",
		Owner:          testOwner(),
		Branch:         repository.CreateBranch{Name: "darkstar/dar-68-test", Base: base},
	}
	worktree, err := manager.Attach(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if branch, ok := worktree.Checkout.(repository.BranchCheckout); !ok || branch.Name != "darkstar/dar-68-test" {
		t.Fatalf("checkout = %#v, want owned branch", worktree.Checkout)
	}
	if _, ok := worktree.Condition.(repository.Clean); !ok {
		t.Fatalf("condition = %#v, want clean", worktree.Condition)
	}
	if worktree.HeadSHA != base.CommitSHA {
		t.Fatalf("worktree head = %q, want %q", worktree.HeadSHA, base.CommitSHA)
	}
	if got := gitOutput(t, fixture.repository, "status", "--porcelain=v1", "--untracked-files=all"); got != before {
		t.Fatalf("user checkout status changed from %q to %q", before, got)
	}
	if got := strings.TrimSpace(gitOutput(t, fixture.repository, "branch", "--show-current")); got != "main" {
		t.Fatalf("user checkout branch = %q, want main", got)
	}

	reconciled, err := manager.Attach(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Path != worktree.Path {
		t.Fatalf("reconciled path = %q, want %q", reconciled.Path, worktree.Path)
	}

	removal := repository.RemoveRequest{
		RepositoryPath:  fixture.repository,
		WorktreePath:    worktreePath,
		OperationID:     "operation_remove_1",
		Owner:           testOwner(),
		BranchName:      "darkstar/dar-68-test",
		ExpectedHeadSHA: base.CommitSHA,
	}
	removed, err := manager.Remove(context.Background(), removal)
	if err != nil {
		t.Fatal(err)
	}
	if removed.AlreadyAbsent {
		t.Fatal("first removal reported already absent")
	}
	if _, err := os.Stat(worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists after removal: %v", err)
	}
	if got := strings.TrimSpace(gitOutput(t, fixture.repository, "show-ref", "--hash", "refs/heads/darkstar/dar-68-test")); got != base.CommitSHA {
		t.Fatalf("preserved branch tip = %q, want %q", got, base.CommitSHA)
	}
	removed, err = manager.Remove(context.Background(), removal)
	if err != nil {
		t.Fatal(err)
	}
	if !removed.AlreadyAbsent {
		t.Fatal("second removal did not reconcile already-absent worktree")
	}
}

func TestRemoveRefusesDirtyOwnedWorktreeWithoutChangingIt(t *testing.T) {
	t.Parallel()
	fixture := newRepository(t)
	manager := newManager(t)
	base, err := manager.ResolveBase(context.Background(), repository.ResolveBaseRequest{RepositoryPath: fixture.repository, BaseRef: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(fixture.root, "worktrees", "dirty")
	worktree, err := manager.Attach(context.Background(), repository.AttachRequest{
		RepositoryPath: fixture.repository, WorktreePath: worktreePath, OperationID: "operation_attach_dirty",
		Owner: testOwner(), Branch: repository.CreateBranch{Name: "darkstar/dirty", Base: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	dirtyFile := filepath.Join(worktree.Path, "candidate.txt")
	if err := os.WriteFile(dirtyFile, []byte("candidate evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := manager.Inspect(context.Background(), repository.InspectRequest{Path: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	found := findWorktree(t, observation.Worktrees, worktreePath)
	if dirty, ok := found.Condition.(repository.Dirty); !ok || len(dirty.Changes) == 0 {
		t.Fatalf("condition = %#v, want dirty changes", found.Condition)
	}

	_, err = manager.Remove(context.Background(), repository.RemoveRequest{
		RepositoryPath: fixture.repository, WorktreePath: worktreePath, OperationID: "operation_remove_dirty",
		Owner: testOwner(), BranchName: "darkstar/dirty", ExpectedHeadSHA: base.CommitSHA,
	})
	assertFailureCode(t, err, ports.FailureConflict)
	if content, readErr := os.ReadFile(dirtyFile); readErr != nil || string(content) != "candidate evidence\n" {
		t.Fatalf("dirty evidence changed: content=%q err=%v", content, readErr)
	}
	if got := strings.TrimSpace(gitOutput(t, worktreePath, "branch", "--show-current")); got != "darkstar/dirty" {
		t.Fatalf("dirty worktree branch = %q, want preserved owned branch", got)
	}
}

func TestAttachRejectsUnownedBranchCollisionBeforeMutation(t *testing.T) {
	t.Parallel()
	fixture := newRepository(t)
	manager := newManager(t)
	base, err := manager.ResolveBase(context.Background(), repository.ResolveBaseRequest{RepositoryPath: fixture.repository, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, fixture.repository, "branch", "darkstar/collision", base.CommitSHA)
	target := filepath.Join(fixture.root, "worktrees", "collision")
	_, err = manager.Attach(context.Background(), repository.AttachRequest{
		RepositoryPath: fixture.repository, WorktreePath: target, OperationID: "operation_collision",
		Owner: testOwner(), Branch: repository.CreateBranch{Name: "darkstar/collision", Base: base},
	})
	assertFailureCode(t, err, ports.FailureConflict)
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("collision target was mutated: %v", statErr)
	}
	observation, err := manager.Inspect(context.Background(), repository.InspectRequest{Path: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Worktrees) != 1 {
		t.Fatalf("worktree count = %d, want only user checkout", len(observation.Worktrees))
	}
}

func TestReattachRequiresExactRecordedTip(t *testing.T) {
	t.Parallel()
	fixture := newRepository(t)
	manager := newManager(t)
	base, err := manager.ResolveBase(context.Background(), repository.ResolveBaseRequest{RepositoryPath: fixture.repository, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, fixture.repository, "branch", "darkstar/owned", base.CommitSHA)
	wrongTip := strings.Repeat("0", len(base.CommitSHA))
	_, err = manager.Attach(context.Background(), repository.AttachRequest{
		RepositoryPath: fixture.repository, WorktreePath: filepath.Join(fixture.root, "worktrees", "wrong"), OperationID: "operation_wrong_tip",
		Owner: testOwner(), Branch: repository.ReattachBranch{Name: "darkstar/owned", ExpectedCommitSHA: wrongTip},
	})
	assertFailureCode(t, err, ports.FailureNotFound)

	target := filepath.Join(fixture.root, "worktrees", "owned")
	worktree, err := manager.Attach(context.Background(), repository.AttachRequest{
		RepositoryPath: fixture.repository, WorktreePath: target, OperationID: "operation_reattach",
		Owner: testOwner(), Branch: repository.ReattachBranch{Name: "darkstar/owned", ExpectedCommitSHA: base.CommitSHA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if worktree.HeadSHA != base.CommitSHA || checkoutBranch(worktree.Checkout) != "darkstar/owned" {
		t.Fatalf("reattached worktree = %#v, want exact recorded branch and tip", worktree)
	}
}

func TestRemoveRefusesLockedWorktree(t *testing.T) {
	t.Parallel()
	fixture := newRepository(t)
	manager := newManager(t)
	base, err := manager.ResolveBase(context.Background(), repository.ResolveBaseRequest{RepositoryPath: fixture.repository, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.root, "worktrees", "locked")
	_, err = manager.Attach(context.Background(), repository.AttachRequest{
		RepositoryPath: fixture.repository, WorktreePath: target, OperationID: "operation_attach_locked",
		Owner: testOwner(), Branch: repository.CreateBranch{Name: "darkstar/locked", Base: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, fixture.repository, "worktree", "lock", "--reason", "active owner", target)
	observation, err := manager.Inspect(context.Background(), repository.InspectRequest{Path: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	locked := findWorktree(t, observation.Worktrees, target)
	if state, ok := locked.Lock.(repository.Locked); !ok || state.Reason != "active owner" {
		t.Fatalf("lock = %#v, want active owner", locked.Lock)
	}
	_, err = manager.Remove(context.Background(), repository.RemoveRequest{
		RepositoryPath: fixture.repository, WorktreePath: target, OperationID: "operation_remove_locked",
		Owner: testOwner(), BranchName: "darkstar/locked", ExpectedHeadSHA: base.CommitSHA,
	})
	assertFailureCode(t, err, ports.FailureConflict)
}

func TestParseWorktreeListPreservesSpacesAndClosedStates(t *testing.T) {
	t.Parallel()
	lockedReason := "active owner"
	input := "worktree C:/repo with spaces\x00HEAD abc123\x00branch refs/heads/main\x00locked " + lockedReason + "\x00\x00" +
		"worktree C:/detached\x00HEAD def456\x00detached\x00\x00"
	records, err := parseWorktreeList([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].path != "C:/repo with spaces" || records[0].locked == nil || *records[0].locked != lockedReason {
		t.Fatalf("records = %#v", records)
	}
	if _, ok := records[1].checkout().(repository.DetachedCheckout); !ok {
		t.Fatalf("detached checkout = %#v", records[1].checkout())
	}
}

type repositoryFixture struct {
	root       string
	repository string
}

func newRepository(t *testing.T) repositoryFixture {
	t.Helper()
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	if err := os.Mkdir(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repositoryPath, "init", "--initial-branch=main", "--template=")
	gitRun(t, repositoryPath, "config", "user.name", "DARKSTAR Test")
	gitRun(t, repositoryPath, "config", "user.email", "tests@darkstar.local")
	gitRun(t, repositoryPath, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repositoryPath, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repositoryPath, "add", "--all")
	gitRun(t, repositoryPath, "commit", "--no-verify", "-m", "test: base")
	if _, err := canonicalExistingPath(repositoryPath); err != nil {
		t.Fatalf("canonicalize test repository %q: %v", repositoryPath, err)
	}
	return repositoryFixture{root: root, repository: repositoryPath}
}

func newManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func testOwner() repository.Ownership {
	return repository.Ownership{DeliveryLineID: "delivery_test", WorkItemID: "work_test"}
}

func findWorktree(t *testing.T, worktrees []repository.Worktree, path string) repository.Worktree {
	t.Helper()
	target, err := canonicalProspectivePath(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, worktree := range worktrees {
		if pathsEqual(worktree.Path, target) {
			return worktree
		}
	}
	t.Fatalf("worktree %q not found in %#v", target, worktrees)
	return repository.Worktree{}
}

func assertFailureCode(t *testing.T, err error, want ports.FailureCode) {
	t.Helper()
	var failure *ports.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want ports.Failure", err)
	}
	if failure.Code != want {
		t.Fatalf("failure code = %q, want %q", failure.Code, want)
	}
}

func gitRun(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	_ = gitOutput(t, directory, arguments...)
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	configureCommand(command)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", arguments[0], err, stderr.String())
	}
	return string(output)
}
