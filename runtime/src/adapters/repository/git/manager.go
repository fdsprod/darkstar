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
	if manager == nil || manager.executable == "" {
		return commandResult{}, errors.New("git repository manager is not configured")
	}
	command := exec.CommandContext(ctx, manager.executable, arguments...)
	command.Dir = directory
	command.Stdin = nil
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
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
