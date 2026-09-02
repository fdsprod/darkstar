// Package git implements the repository manager with the Git command-line client.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"darkstar/src/ports"
	"darkstar/src/ports/repository"
)

// Manager owns conservative local Git observations and worktree mutations.
type Manager struct {
	executable string
}

var _ repository.Manager = (*Manager)(nil)

// New resolves and pins the Git executable used by the manager. An empty value
// discovers git from PATH once during construction.
func New(executable string) (*Manager, error) {
	target := strings.TrimSpace(executable)
	if target == "" {
		target = "git"
	}
	resolved, err := exec.LookPath(target)
	if err != nil {
		return nil, failure(ports.FailureUnavailable, "Git is not executable", true, nil)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, failure(ports.FailureInvalidRequest, "Git executable path is invalid", false, nil)
	}
	return &Manager{executable: filepath.Clean(resolved)}, nil
}

// Inspect returns canonical repository identity and normalized worktree state
// without fetching or changing Git metadata.
func (manager *Manager) Inspect(ctx context.Context, request repository.InspectRequest) (repository.Observation, error) {
	identity, err := manager.discover(ctx, request.Path)
	if err != nil {
		return repository.Observation{}, err
	}
	worktrees, err := manager.worktrees(ctx, identity)
	if err != nil {
		return repository.Observation{}, err
	}
	return repository.Observation{Repository: identity, Worktrees: worktrees}, nil
}

// ResolveBase freezes one ref at the exact commit currently visible locally.
func (manager *Manager) ResolveBase(ctx context.Context, request repository.ResolveBaseRequest) (repository.BaseRevision, error) {
	if strings.TrimSpace(request.BaseRef) != request.BaseRef || request.BaseRef == "" {
		return repository.BaseRevision{}, invalid("base ref is required")
	}
	identity, err := manager.discover(ctx, request.RepositoryPath)
	if err != nil {
		return repository.BaseRevision{}, err
	}
	commit, err := manager.resolveCommit(ctx, identity.Root, request.BaseRef)
	if err != nil {
		return repository.BaseRevision{}, err
	}
	return repository.BaseRevision{Repository: identity, Ref: request.BaseRef, CommitSHA: commit}, nil
}

// Attach creates or reconciles an isolated worktree without changing the user's checkout.
func (manager *Manager) Attach(ctx context.Context, request repository.AttachRequest) (repository.Worktree, error) {
	if err := validateMutation(request.RepositoryPath, request.WorktreePath, request.OperationID, request.Owner); err != nil {
		return repository.Worktree{}, err
	}
	identity, err := manager.discover(ctx, request.RepositoryPath)
	if err != nil {
		return repository.Worktree{}, err
	}
	target, err := canonicalProspectivePath(request.WorktreePath)
	if err != nil {
		return repository.Worktree{}, invalid("worktree path is invalid")
	}

	branchName, expectedCommit, create, err := branchPlan(request.Branch, identity)
	if err != nil {
		return repository.Worktree{}, err
	}
	if err := manager.checkBranchName(ctx, identity.Root, branchName); err != nil {
		return repository.Worktree{}, err
	}
	expectedCommit, err = manager.resolveCommit(ctx, identity.Root, expectedCommit)
	if err != nil {
		return repository.Worktree{}, err
	}

	observation, err := manager.Inspect(ctx, repository.InspectRequest{Path: identity.Root})
	if err != nil {
		return repository.Worktree{}, err
	}
	for _, worktree := range observation.Worktrees {
		if pathsEqual(worktree.Path, target) {
			if exactAttachment(worktree, branchName, expectedCommit) {
				return worktree, nil
			}
			return repository.Worktree{}, conflict("worktree path is already registered with different state")
		}
		if checkoutBranch(worktree.Checkout) == branchName {
			return repository.Worktree{}, conflict("branch is already attached to another worktree")
		}
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		return repository.Worktree{}, conflict("worktree path already exists outside the requested attachment")
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return repository.Worktree{}, failure(ports.FailureUnavailable, "worktree path cannot be inspected", true, nil)
	}

	branchCommit, branchExists, err := manager.localBranch(ctx, identity.Root, branchName)
	if err != nil {
		return repository.Worktree{}, err
	}
	if create && branchExists {
		return repository.Worktree{}, conflict("branch name already exists without matching attachment ownership")
	}
	if !create && !branchExists {
		return repository.Worktree{}, failure(ports.FailureNotFound, "owned branch does not exist", false, nil)
	}
	if !create && branchCommit != expectedCommit {
		return repository.Worktree{}, conflict("owned branch tip does not match its recorded commit")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return repository.Worktree{}, failure(ports.FailureUnavailable, "worktree parent directory cannot be created", true, nil)
	}

	arguments := []string{"worktree", "add"}
	if create {
		arguments = append(arguments, "--no-track", "-b", branchName, "--", target, expectedCommit)
	} else {
		arguments = append(arguments, "--", target, branchName)
	}
	if _, err := manager.run(ctx, identity.Root, arguments...); err != nil {
		return repository.Worktree{}, failure(ports.FailureUncertain, "Git could not prove the worktree attachment result", false, nil)
	}

	after, err := manager.Inspect(ctx, repository.InspectRequest{Path: identity.Root})
	if err != nil {
		return repository.Worktree{}, failure(ports.FailureUncertain, "created worktree could not be inspected", false, nil)
	}
	for _, worktree := range after.Worktrees {
		if pathsEqual(worktree.Path, target) && exactAttachment(worktree, branchName, expectedCommit) {
			return worktree, nil
		}
	}
	return repository.Worktree{}, failure(ports.FailureUncertain, "created worktree does not match the requested attachment", false, nil)
}

// CaptureCandidate freezes the exact worktree tree without changing the real
// index. The returned tree object and manifest are safe immutable validator input.
func (manager *Manager) CaptureCandidate(ctx context.Context, request repository.CaptureCandidateRequest) (repository.Candidate, error) {
	if err := validateCandidateLocation(request.RepositoryPath, request.WorktreePath, request.BranchName, request.ExpectedParentSHA); err != nil {
		return repository.Candidate{}, err
	}
	identity, worktree, parent, err := manager.ownedCandidateWorktree(ctx, request.RepositoryPath, request.WorktreePath, request.BranchName, request.ExpectedParentSHA)
	if err != nil {
		return repository.Candidate{}, err
	}
	if _, dirty := worktree.Condition.(repository.Dirty); !dirty {
		if _, clean := worktree.Condition.(repository.Clean); clean {
			return repository.Candidate{}, invalid("point candidate has no changes")
		}
		return repository.Candidate{}, conflict("point candidate worktree is not an ordinary dirty workspace")
	}

	tree, manifest, err := manager.candidateTree(ctx, worktree, parent)
	if err != nil {
		return repository.Candidate{}, err
	}
	parentTree, err := manager.resolveTree(ctx, worktree.Path, parent)
	if err != nil {
		return repository.Candidate{}, err
	}
	if tree == parentTree || len(manifest) == 0 {
		return repository.Candidate{}, invalid("point candidate has no committable changes")
	}
	return repository.Candidate{
		Repository: identity, WorktreePath: worktree.Path, BranchName: request.BranchName,
		ParentSHA: parent, TreeSHA: tree, Manifest: manifest,
	}, nil
}

// CommitCandidate creates one owned commit and advances the branch with an
// update-ref compare-and-swap. A retry adopts only an exact trailer/tree match.
func (manager *Manager) CommitCandidate(ctx context.Context, request repository.CommitCandidateRequest) (repository.PointCommit, error) {
	if err := validateCommitRequest(request); err != nil {
		return repository.PointCommit{}, err
	}
	identity, worktree, parent, err := manager.ownedCandidateWorktree(ctx, request.Candidate.Repository.Root, request.Candidate.WorktreePath, request.Candidate.BranchName, request.Candidate.ParentSHA)
	if err != nil {
		// A changed HEAD is the expected retry shape, so reconcile it separately.
		identity, worktree, err = manager.findCandidateWorktree(ctx, request.Candidate.Repository.Root, request.Candidate.WorktreePath, request.Candidate.BranchName)
		if err != nil {
			return repository.PointCommit{}, err
		}
		parent, err = manager.resolveCommit(ctx, identity.Root, request.Candidate.ParentSHA)
		if err != nil {
			return repository.PointCommit{}, err
		}
	}
	if !sameRepository(identity, request.Candidate.Repository) {
		return repository.PointCommit{}, conflict("point candidate belongs to a different repository")
	}
	tree, err := manager.resolveTree(ctx, worktree.Path, request.Candidate.TreeSHA)
	if err != nil || tree != request.Candidate.TreeSHA {
		return repository.PointCommit{}, invalid("point candidate tree is unavailable or invalid")
	}
	message := pointCommitMessage(request)
	if worktree.HeadSHA != parent {
		return manager.reconcilePointCommit(ctx, worktree, request, parent, message)
	}

	current, err := manager.CaptureCandidate(ctx, repository.CaptureCandidateRequest{
		RepositoryPath: identity.Root, WorktreePath: worktree.Path, BranchName: request.Candidate.BranchName, ExpectedParentSHA: parent,
	})
	if err != nil {
		return repository.PointCommit{}, err
	}
	if current.TreeSHA != request.Candidate.TreeSHA || !equalStrings(current.Manifest, request.Candidate.Manifest) {
		return repository.PointCommit{}, conflict("point candidate changed after validation")
	}

	created, err := manager.runWith(ctx, worktree.Path, []byte(message), nil, "commit-tree", request.Candidate.TreeSHA, "-p", parent)
	if err != nil {
		return repository.PointCommit{}, failure(ports.FailureUnavailable, "Git could not create the point commit", true, nil)
	}
	commitSHA := strings.TrimSpace(string(created.stdout))
	if commitSHA == "" || strings.ContainsAny(commitSHA, "\r\n \t") {
		return repository.PointCommit{}, failure(ports.FailureProtocolDrift, "Git returned an invalid point commit identity", false, nil)
	}
	ref := "refs/heads/" + request.Candidate.BranchName
	if _, err := manager.run(ctx, worktree.Path, "update-ref", "-m", "darkstar point "+request.OperationID, ref, commitSHA, parent); err != nil {
		_, currentWorktree, inspectErr := manager.findCandidateWorktree(ctx, identity.Root, worktree.Path, request.Candidate.BranchName)
		if inspectErr == nil {
			observed, reconcileErr := manager.reconcilePointCommit(ctx, currentWorktree, request, parent, message)
			if reconcileErr == nil {
				return observed, nil
			}
		}
		return repository.PointCommit{}, failure(ports.FailureUncertain, "Git could not prove the point commit ref update", false, nil)
	}
	if err := manager.synchronizeCommittedWorktree(ctx, worktree.Path, request.Candidate.BranchName, commitSHA, request.Candidate.TreeSHA); err != nil {
		return repository.PointCommit{}, err
	}
	return repository.PointCommit{CommitSHA: commitSHA, ParentSHA: parent, TreeSHA: request.Candidate.TreeSHA}, nil
}

func (manager *Manager) reconcilePointCommit(ctx context.Context, worktree repository.Worktree, request repository.CommitCandidateRequest, parent, message string) (repository.PointCommit, error) {
	ancestor, err := manager.run(ctx, worktree.Path, "merge-base", "--is-ancestor", parent, worktree.HeadSHA)
	if err != nil {
		if ancestor.exitCode == 1 {
			return repository.PointCommit{}, conflict("owned branch no longer descends from the candidate parent")
		}
		return repository.PointCommit{}, failure(ports.FailureUnavailable, "owned branch ancestry cannot be inspected", true, nil)
	}
	listed, err := manager.run(ctx, worktree.Path, "rev-list", "--first-parent", worktree.HeadSHA, "^"+parent)
	if err != nil {
		return repository.PointCommit{}, failure(ports.FailureUnavailable, "owned point commits cannot be inspected", true, nil)
	}
	var match string
	for _, commitSHA := range strings.Fields(string(listed.stdout)) {
		record, inspectErr := manager.run(ctx, worktree.Path, "show", "-s", "--format=%P%x00%T%x00%B", commitSHA)
		if inspectErr != nil {
			return repository.PointCommit{}, failure(ports.FailureUnavailable, "owned point commit cannot be inspected", true, nil)
		}
		parts := strings.SplitN(string(record.stdout), "\x00", 3)
		if len(parts) != 3 || strings.TrimSpace(parts[0]) != parent || strings.TrimSpace(parts[1]) != request.Candidate.TreeSHA || strings.TrimSpace(parts[2]) != strings.TrimSpace(message) {
			continue
		}
		if match != "" {
			return repository.PointCommit{}, conflict("multiple commits match the point operation identity")
		}
		match = commitSHA
	}
	if match == "" {
		return repository.PointCommit{}, conflict("owned branch advanced without the expected point commit")
	}
	if worktree.HeadSHA == match {
		if err := manager.synchronizeCommittedWorktree(ctx, worktree.Path, request.Candidate.BranchName, match, request.Candidate.TreeSHA); err != nil {
			return repository.PointCommit{}, err
		}
	}
	return repository.PointCommit{CommitSHA: match, ParentSHA: parent, TreeSHA: request.Candidate.TreeSHA, AlreadyPresent: true}, nil
}

func (manager *Manager) synchronizeCommittedWorktree(ctx context.Context, path, branch, commitSHA, treeSHA string) error {
	if _, err := manager.run(ctx, path, "read-tree", treeSHA); err != nil {
		return failure(ports.FailureUncertain, "point commit exists but its worktree index requires reconciliation", false, nil)
	}
	observation, err := manager.Inspect(ctx, repository.InspectRequest{Path: path})
	if err != nil {
		return failure(ports.FailureUncertain, "point commit worktree cannot be reconciled", false, nil)
	}
	for _, current := range observation.Worktrees {
		if pathsEqual(current.Path, path) && checkoutBranch(current.Checkout) == branch && current.HeadSHA == commitSHA {
			if _, clean := current.Condition.(repository.Clean); clean {
				return nil
			}
		}
	}
	return failure(ports.FailureUncertain, "point commit worktree is not clean at the committed tree", false, nil)
}

func (manager *Manager) candidateTree(ctx context.Context, worktree repository.Worktree, parent string) (string, []string, error) {
	temporary, err := os.CreateTemp(worktree.GitDir, ".darkstar-index-*")
	if err != nil {
		return "", nil, failure(ports.FailureUnavailable, "temporary candidate index cannot be created", true, nil)
	}
	indexPath := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		_ = os.Remove(indexPath)
		return "", nil, failure(ports.FailureUnavailable, "temporary candidate index cannot be closed", true, nil)
	}
	if removeErr := os.Remove(indexPath); removeErr != nil {
		return "", nil, failure(ports.FailureUnavailable, "temporary candidate index cannot be initialized", true, nil)
	}
	defer func() { _ = os.Remove(indexPath) }()
	environment := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := manager.runWith(ctx, worktree.Path, nil, environment, "read-tree", parent); err != nil {
		return "", nil, failure(ports.FailureUnavailable, "candidate parent tree cannot be loaded", true, nil)
	}
	if _, err := manager.runWith(ctx, worktree.Path, nil, environment, "add", "--all", "--", "."); err != nil {
		return "", nil, failure(ports.FailureUnavailable, "point candidate cannot be inventoried", true, nil)
	}
	written, err := manager.runWith(ctx, worktree.Path, nil, environment, "write-tree")
	if err != nil {
		return "", nil, failure(ports.FailureUnavailable, "point candidate tree cannot be frozen", true, nil)
	}
	tree := strings.TrimSpace(string(written.stdout))
	if tree == "" || strings.ContainsAny(tree, "\r\n \t") {
		return "", nil, failure(ports.FailureProtocolDrift, "Git returned an invalid candidate tree identity", false, nil)
	}
	changed, err := manager.run(ctx, worktree.Path, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", parent, tree)
	if err != nil {
		return "", nil, failure(ports.FailureUnavailable, "point candidate manifest cannot be created", true, nil)
	}
	manifest := splitNUL(changed.stdout)
	sort.Strings(manifest)
	return tree, manifest, nil
}

// Remove detaches only an exact, clean, unlocked worktree and preserves its branch.
func (manager *Manager) Remove(ctx context.Context, request repository.RemoveRequest) (repository.Removal, error) {
	if err := validateMutation(request.RepositoryPath, request.WorktreePath, request.OperationID, request.Owner); err != nil {
		return repository.Removal{}, err
	}
	if strings.TrimSpace(request.BranchName) != request.BranchName || request.BranchName == "" || strings.TrimSpace(request.ExpectedHeadSHA) == "" {
		return repository.Removal{}, invalid("branch name and expected head commit are required")
	}
	identity, err := manager.discover(ctx, request.RepositoryPath)
	if err != nil {
		return repository.Removal{}, err
	}
	target, err := canonicalProspectivePath(request.WorktreePath)
	if err != nil {
		return repository.Removal{}, invalid("worktree path is invalid")
	}
	expected, err := manager.resolveCommit(ctx, identity.Root, request.ExpectedHeadSHA)
	if err != nil {
		return repository.Removal{}, err
	}
	observation, err := manager.Inspect(ctx, repository.InspectRequest{Path: identity.Root})
	if err != nil {
		return repository.Removal{}, err
	}
	var selected *repository.Worktree
	for index := range observation.Worktrees {
		if pathsEqual(observation.Worktrees[index].Path, target) {
			selected = &observation.Worktrees[index]
			break
		}
	}
	if selected == nil {
		if _, statErr := os.Lstat(target); errors.Is(statErr, fs.ErrNotExist) {
			return repository.Removal{Repository: identity, WorktreePath: target, AlreadyAbsent: true}, nil
		}
		return repository.Removal{}, conflict("unregistered worktree path still exists and will not be removed")
	}
	if checkoutBranch(selected.Checkout) != request.BranchName {
		return repository.Removal{}, conflict("worktree branch does not match its recorded ownership")
	}
	if selected.HeadSHA != expected {
		return repository.Removal{}, conflict("worktree head does not match its recorded ownership")
	}
	if _, clean := selected.Condition.(repository.Clean); !clean {
		return repository.Removal{}, conflict("worktree is not clean and will not be removed")
	}
	if _, unlocked := selected.Lock.(repository.Unlocked); !unlocked {
		return repository.Removal{}, conflict("worktree is locked and will not be removed")
	}
	if selected.PrunableReason != "" {
		return repository.Removal{}, conflict("worktree is prunable or missing and requires reconciliation")
	}
	if _, err := manager.run(ctx, identity.Root, "worktree", "remove", "--", target); err != nil {
		return repository.Removal{}, failure(ports.FailureUncertain, "Git could not prove the worktree removal result", false, nil)
	}
	after, err := manager.Inspect(ctx, repository.InspectRequest{Path: identity.Root})
	if err != nil {
		return repository.Removal{}, failure(ports.FailureUncertain, "removed worktree could not be reconciled", false, nil)
	}
	for _, worktree := range after.Worktrees {
		if pathsEqual(worktree.Path, target) {
			return repository.Removal{}, failure(ports.FailureUncertain, "worktree remains registered after removal", false, nil)
		}
	}
	branchCommit, exists, err := manager.localBranch(ctx, identity.Root, request.BranchName)
	if err != nil || !exists || branchCommit != expected {
		return repository.Removal{}, failure(ports.FailureUncertain, "owned branch was not preserved at its recorded tip", false, nil)
	}
	return repository.Removal{Repository: identity, WorktreePath: target}, nil
}

func (manager *Manager) discover(ctx context.Context, path string) (repository.Identity, error) {
	if strings.TrimSpace(path) != path || path == "" || !filepath.IsAbs(path) {
		return repository.Identity{}, invalid("repository path must be absolute")
	}
	rootResult, err := manager.run(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return repository.Identity{}, failure(ports.FailureNotFound, "path is not a non-bare Git worktree", false, nil)
	}
	commonResult, err := manager.run(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repository.Identity{}, failure(ports.FailureUnavailable, "Git common directory cannot be resolved", false, nil)
	}
	root, err := canonicalExistingPath(strings.TrimSpace(string(rootResult.stdout)))
	if err != nil {
		return repository.Identity{}, failure(ports.FailureUnavailable, "repository root cannot be canonicalized", false, nil)
	}
	common, err := canonicalExistingPath(strings.TrimSpace(string(commonResult.stdout)))
	if err != nil {
		return repository.Identity{}, failure(ports.FailureUnavailable, "Git common directory cannot be canonicalized", false, nil)
	}
	return repository.Identity{Root: root, CommonGitDir: common}, nil
}

func (manager *Manager) worktrees(ctx context.Context, identity repository.Identity) ([]repository.Worktree, error) {
	result, err := manager.run(ctx, identity.Root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, failure(ports.FailureUnavailable, "Git worktrees cannot be enumerated", true, nil)
	}
	records, err := parseWorktreeList(result.stdout)
	if err != nil {
		return nil, failure(ports.FailureProtocolDrift, "Git returned an invalid worktree inventory", false, nil)
	}
	values := make([]repository.Worktree, 0, len(records))
	for _, record := range records {
		path, canonicalErr := canonicalProspectivePath(record.path)
		if canonicalErr != nil {
			return nil, failure(ports.FailureProtocolDrift, "Git returned an invalid worktree path", false, nil)
		}
		value := repository.Worktree{
			Path: path, HeadSHA: record.head, Checkout: record.checkout(), Lock: record.lock(), PrunableReason: record.prunable,
		}
		if record.prunable != "" {
			value.Condition = repository.Unavailable{Reason: record.prunable}
			values = append(values, value)
			continue
		}
		gitDirResult, inspectErr := manager.run(ctx, path, "rev-parse", "--path-format=absolute", "--absolute-git-dir")
		if inspectErr != nil {
			value.Condition = repository.Unavailable{Reason: "worktree Git directory is unavailable"}
			values = append(values, value)
			continue
		}
		value.GitDir, inspectErr = canonicalExistingPath(strings.TrimSpace(string(gitDirResult.stdout)))
		if inspectErr != nil {
			value.Condition = repository.Unavailable{Reason: "worktree Git directory cannot be canonicalized"}
			values = append(values, value)
			continue
		}
		value.Condition, inspectErr = manager.condition(ctx, path, value.GitDir)
		if inspectErr != nil {
			return nil, inspectErr
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return normalizedPath(values[i].Path) < normalizedPath(values[j].Path) })
	return values, nil
}

func (manager *Manager) condition(ctx context.Context, path, gitDir string) (repository.Condition, error) {
	result, err := manager.run(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return nil, failure(ports.FailureUnavailable, "worktree status cannot be inspected", true, nil)
	}
	changes := splitNUL(result.stdout)
	if operation := gitOperation(gitDir); operation != "" {
		return repository.OperationInProgress{Operation: operation, Changes: changes}, nil
	}
	for _, change := range changes {
		if conflictStatus(change) {
			return repository.Conflicted{Changes: changes}, nil
		}
	}
	if len(changes) != 0 {
		return repository.Dirty{Changes: changes}, nil
	}
	return repository.Clean{}, nil
}

func (manager *Manager) resolveCommit(ctx context.Context, root, revision string) (string, error) {
	if strings.TrimSpace(revision) != revision || revision == "" {
		return "", invalid("commit or ref is required")
	}
	result, err := manager.run(ctx, root, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", failure(ports.FailureNotFound, "commit or ref cannot be resolved", false, nil)
	}
	commit := strings.TrimSpace(string(result.stdout))
	if commit == "" || strings.ContainsAny(commit, "\r\n \t") {
		return "", failure(ports.FailureProtocolDrift, "Git returned an invalid commit identity", false, nil)
	}
	return commit, nil
}

func (manager *Manager) localBranch(ctx context.Context, root, name string) (string, bool, error) {
	ref := "refs/heads/" + name
	result, err := manager.run(ctx, root, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		if result.exitCode == 1 {
			return "", false, nil
		}
		return "", false, failure(ports.FailureUnavailable, "local branch cannot be inspected", true, nil)
	}
	commit, err := manager.resolveCommit(ctx, root, ref)
	if err != nil {
		return "", false, err
	}
	return commit, true, nil
}

func (manager *Manager) checkBranchName(ctx context.Context, root, name string) error {
	if strings.TrimSpace(name) != name || name == "" {
		return invalid("branch name is required")
	}
	if _, err := manager.run(ctx, root, "check-ref-format", "--branch", name); err != nil {
		return invalid("branch name is not valid for Git")
	}
	return nil
}

func branchPlan(plan repository.BranchPlan, identity repository.Identity) (string, string, bool, error) {
	switch value := plan.(type) {
	case repository.CreateBranch:
		if !sameRepository(value.Base.Repository, identity) {
			return "", "", false, conflict("frozen base belongs to a different repository")
		}
		return value.Name, value.Base.CommitSHA, true, nil
	case *repository.CreateBranch:
		if value == nil {
			return "", "", false, invalid("branch plan is required")
		}
		if !sameRepository(value.Base.Repository, identity) {
			return "", "", false, conflict("frozen base belongs to a different repository")
		}
		return value.Name, value.Base.CommitSHA, true, nil
	case repository.ReattachBranch:
		return value.Name, value.ExpectedCommitSHA, false, nil
	case *repository.ReattachBranch:
		if value == nil {
			return "", "", false, invalid("branch plan is required")
		}
		return value.Name, value.ExpectedCommitSHA, false, nil
	default:
		return "", "", false, invalid("branch plan is required")
	}
}

func validateMutation(repositoryPath, worktreePath, operationID string, owner repository.Ownership) error {
	if strings.TrimSpace(repositoryPath) != repositoryPath || repositoryPath == "" || !filepath.IsAbs(repositoryPath) {
		return invalid("repository path must be absolute")
	}
	if strings.TrimSpace(worktreePath) != worktreePath || worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return invalid("worktree path must be absolute")
	}
	if strings.TrimSpace(operationID) != operationID || operationID == "" ||
		strings.TrimSpace(owner.DeliveryLineID) != owner.DeliveryLineID || owner.DeliveryLineID == "" ||
		strings.TrimSpace(owner.WorkItemID) != owner.WorkItemID || owner.WorkItemID == "" {
		return invalid("operation, delivery-line, and work-item identities are required")
	}
	return nil
}

func validateCandidateLocation(repositoryPath, worktreePath, branchName, parent string) error {
	if strings.TrimSpace(repositoryPath) != repositoryPath || repositoryPath == "" || !filepath.IsAbs(repositoryPath) {
		return invalid("repository path must be absolute")
	}
	if strings.TrimSpace(worktreePath) != worktreePath || worktreePath == "" || !filepath.IsAbs(worktreePath) {
		return invalid("worktree path must be absolute")
	}
	if strings.TrimSpace(branchName) != branchName || branchName == "" || strings.TrimSpace(parent) != parent || parent == "" {
		return invalid("branch name and expected parent commit are required")
	}
	return nil
}

func validateCommitRequest(request repository.CommitCandidateRequest) error {
	if err := validateCandidateLocation(request.Candidate.Repository.Root, request.Candidate.WorktreePath, request.Candidate.BranchName, request.Candidate.ParentSHA); err != nil {
		return err
	}
	if strings.TrimSpace(request.Candidate.Repository.CommonGitDir) == "" || strings.TrimSpace(request.Candidate.TreeSHA) != request.Candidate.TreeSHA || request.Candidate.TreeSHA == "" || len(request.Candidate.Manifest) == 0 {
		return invalid("candidate repository, tree, and manifest are required")
	}
	if err := validateMutation(request.Candidate.Repository.Root, request.Candidate.WorktreePath, request.OperationID, request.Owner); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"operation ID", request.OperationID}, {"work-item ID", request.Owner.WorkItemID}, {"run ID", request.Point.RunID},
		{"story ID", request.Point.StoryID}, {"point ID", request.Point.PointID}, {"commit subject", request.Subject},
	} {
		if strings.TrimSpace(field.value) != field.value || field.value == "" || strings.ContainsAny(field.value, "\r\n\x00") {
			return invalid(field.name + " is required, must be trimmed, and cannot contain line breaks")
		}
	}
	if request.Point.Revision == 0 {
		return invalid("point revision must be greater than zero")
	}
	for index, value := range request.Candidate.Manifest {
		if value == "" || strings.ContainsRune(value, '\x00') || (index > 0 && request.Candidate.Manifest[index-1] >= value) {
			return invalid("candidate manifest must be non-empty, unique, and sorted")
		}
	}
	return nil
}

func (manager *Manager) ownedCandidateWorktree(ctx context.Context, repositoryPath, worktreePath, branchName, parent string) (repository.Identity, repository.Worktree, string, error) {
	identity, worktree, err := manager.findCandidateWorktree(ctx, repositoryPath, worktreePath, branchName)
	if err != nil {
		return repository.Identity{}, repository.Worktree{}, "", err
	}
	parentSHA, err := manager.resolveCommit(ctx, identity.Root, parent)
	if err != nil {
		return repository.Identity{}, repository.Worktree{}, "", err
	}
	if worktree.HeadSHA != parentSHA {
		return repository.Identity{}, repository.Worktree{}, "", conflict("point candidate parent does not match the owned worktree HEAD")
	}
	return identity, worktree, parentSHA, nil
}

func (manager *Manager) findCandidateWorktree(ctx context.Context, repositoryPath, worktreePath, branchName string) (repository.Identity, repository.Worktree, error) {
	identity, err := manager.discover(ctx, repositoryPath)
	if err != nil {
		return repository.Identity{}, repository.Worktree{}, err
	}
	if err := manager.checkBranchName(ctx, identity.Root, branchName); err != nil {
		return repository.Identity{}, repository.Worktree{}, err
	}
	target, err := canonicalExistingPath(worktreePath)
	if err != nil {
		return repository.Identity{}, repository.Worktree{}, failure(ports.FailureNotFound, "owned point worktree is unavailable", false, nil)
	}
	observation, err := manager.Inspect(ctx, repository.InspectRequest{Path: identity.Root})
	if err != nil {
		return repository.Identity{}, repository.Worktree{}, err
	}
	for _, current := range observation.Worktrees {
		if !pathsEqual(current.Path, target) {
			continue
		}
		if checkoutBranch(current.Checkout) != branchName {
			return repository.Identity{}, repository.Worktree{}, conflict("point candidate branch does not match the owned worktree")
		}
		if _, unlocked := current.Lock.(repository.Unlocked); !unlocked || current.PrunableReason != "" {
			return repository.Identity{}, repository.Worktree{}, conflict("point candidate worktree is locked or unavailable")
		}
		return identity, current, nil
	}
	return repository.Identity{}, repository.Worktree{}, failure(ports.FailureNotFound, "owned point worktree is not registered", false, nil)
}

func (manager *Manager) resolveTree(ctx context.Context, root, revision string) (string, error) {
	if strings.TrimSpace(revision) != revision || revision == "" {
		return "", invalid("tree or commit is required")
	}
	result, err := manager.run(ctx, root, "rev-parse", "--verify", "--end-of-options", revision+"^{tree}")
	if err != nil {
		return "", failure(ports.FailureNotFound, "tree or commit cannot be resolved", false, nil)
	}
	tree := strings.TrimSpace(string(result.stdout))
	if tree == "" || strings.ContainsAny(tree, "\r\n \t") {
		return "", failure(ports.FailureProtocolDrift, "Git returned an invalid tree identity", false, nil)
	}
	return tree, nil
}

func pointCommitMessage(request repository.CommitCandidateRequest) string {
	return request.Subject + "\n\n" +
		"Darkstar-Work-Item: " + request.Owner.WorkItemID + "\n" +
		"Darkstar-Run: " + request.Point.RunID + "\n" +
		"Darkstar-Story: " + request.Point.StoryID + "\n" +
		"Darkstar-Point: " + request.Point.PointID + "\n" +
		"Darkstar-Point-Revision: " + strconv.FormatUint(request.Point.Revision, 10) + "\n" +
		"Darkstar-Operation: " + request.OperationID + "\n"
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func exactAttachment(worktree repository.Worktree, branchName, head string) bool {
	if checkoutBranch(worktree.Checkout) != branchName || worktree.HeadSHA != head || worktree.PrunableReason != "" {
		return false
	}
	if _, clean := worktree.Condition.(repository.Clean); !clean {
		return false
	}
	_, unlocked := worktree.Lock.(repository.Unlocked)
	return unlocked
}

func checkoutBranch(checkout repository.Checkout) string {
	if branch, ok := checkout.(repository.BranchCheckout); ok {
		return branch.Name
	}
	return ""
}

func sameRepository(left, right repository.Identity) bool {
	return pathsEqual(left.Root, right.Root) && pathsEqual(left.CommonGitDir, right.CommonGitDir)
}

func canonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(absolute); err != nil {
		return "", err
	}
	evaluated, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		// Some supported Windows filesystems deny the handle operation used by
		// EvalSymlinks for ordinary directories. The absolute cleaned spelling is
		// still stable enough for DS-112; boundary/reparse denial belongs to DS-190.
		return filepath.Clean(absolute), nil
	}
	return filepath.Clean(evaluated), nil
}

func canonicalProspectivePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path is not absolute")
	}
	absolute := filepath.Clean(path)
	ancestor := absolute
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		ancestor = parent
	}
	canonicalAncestor, err := canonicalExistingPath(ancestor)
	if err != nil {
		return "", err
	}
	remainder, err := filepath.Rel(ancestor, absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(canonicalAncestor, remainder)), nil
}

func pathsEqual(left, right string) bool { return normalizedPath(left) == normalizedPath(right) }

func normalizedPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func splitNUL(value []byte) []string {
	parts := bytes.Split(value, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func conflictStatus(value string) bool {
	if len(value) < 2 {
		return false
	}
	switch value[:2] {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}

func gitOperation(gitDir string) string {
	candidates := []struct {
		name string
		path string
	}{
		{name: "rebase", path: "rebase-merge"},
		{name: "rebase", path: "rebase-apply"},
		{name: "merge", path: "MERGE_HEAD"},
		{name: "cherry-pick", path: "CHERRY_PICK_HEAD"},
		{name: "revert", path: "REVERT_HEAD"},
		{name: "bisect", path: "BISECT_LOG"},
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(gitDir, candidate.path)); err == nil {
			return candidate.name
		}
	}
	return ""
}

func invalid(message string) error {
	return failure(ports.FailureInvalidRequest, message, false, nil)
}

func conflict(message string) error {
	return failure(ports.FailureConflict, message, false, nil)
}

func failure(code ports.FailureCode, message string, retryable bool, details map[string]string) error {
	return &ports.Failure{Code: code, Message: message, Retryable: retryable, Details: details}
}

type commandResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

func (manager *Manager) run(ctx context.Context, directory string, arguments ...string) (commandResult, error) {
	return manager.runWith(ctx, directory, nil, nil, arguments...)
}

func (manager *Manager) runWith(ctx context.Context, directory string, input []byte, environment []string, arguments ...string) (commandResult, error) {
	if manager == nil || manager.executable == "" {
		return commandResult{}, errors.New("git repository manager is not configured")
	}
	command := exec.CommandContext(ctx, manager.executable, arguments...)
	command.Dir = directory
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	command.Env = append(os.Environ(), append([]string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never"}, environment...)...)
	configureCommand(command)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		return result, err
	}
	result.exitCode = -1
	return result, err
}

type worktreeRecord struct {
	path     string
	head     string
	branch   string
	detached bool
	bare     bool
	locked   *string
	prunable string
}

func (record worktreeRecord) checkout() repository.Checkout {
	switch {
	case record.bare:
		return repository.BareCheckout{}
	case record.detached:
		return repository.DetachedCheckout{}
	default:
		return repository.BranchCheckout{Name: strings.TrimPrefix(record.branch, "refs/heads/")}
	}
}

func (record worktreeRecord) lock() repository.LockState {
	if record.locked == nil {
		return repository.Unlocked{}
	}
	return repository.Locked{Reason: *record.locked}
}

func parseWorktreeList(output []byte) ([]worktreeRecord, error) {
	tokens := bytes.Split(output, []byte{0})
	values := make([]worktreeRecord, 0)
	var current *worktreeRecord
	flush := func() error {
		if current == nil {
			return nil
		}
		if current.path == "" || (!current.bare && current.head == "") || (!current.bare && !current.detached && current.branch == "") {
			return errors.New("incomplete worktree record")
		}
		values = append(values, *current)
		current = nil
		return nil
	}
	for _, raw := range tokens {
		line := string(raw)
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, found := strings.Cut(line, " ")
		if !found {
			key = line
			value = ""
		}
		if key == "worktree" {
			if err := flush(); err != nil {
				return nil, err
			}
			current = &worktreeRecord{path: value}
			continue
		}
		if current == nil {
			return nil, errors.New("worktree field before record")
		}
		switch key {
		case "HEAD":
			current.head = value
		case "branch":
			current.branch = value
		case "detached":
			current.detached = true
		case "bare":
			current.bare = true
		case "locked":
			current.locked = &value
		case "prunable":
			current.prunable = value
		default:
			return nil, fmt.Errorf("unknown worktree field %q", key)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return values, nil
}
