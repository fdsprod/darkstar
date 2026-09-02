// Package repository defines application-owned Git repository and worktree operations.
package repository

import "context"

// Manager observes repositories, freezes base revisions, and manages isolated worktrees.
// Mutating requests carry durable ownership and operation identities chosen by the caller.
type Manager interface {
	Inspect(context.Context, InspectRequest) (Observation, error)
	ResolveBase(context.Context, ResolveBaseRequest) (BaseRevision, error)
	Attach(context.Context, AttachRequest) (Worktree, error)
	Remove(context.Context, RemoveRequest) (Removal, error)
}

// Identity is the canonical identity of one local repository. Root is the checkout
// selected by the caller; CommonGitDir identifies all linked worktrees together.
type Identity struct {
	Root         string
	CommonGitDir string
}

// Ownership names the durable delivery line and work item allowed to own a branch.
type Ownership struct {
	DeliveryLineID string
	WorkItemID     string
}

// InspectRequest selects any checkout belonging to the repository.
type InspectRequest struct {
	Path string
}

// Observation is a point-in-time inventory of one repository and all registered worktrees.
type Observation struct {
	Repository Identity
	Worktrees  []Worktree
}

// ResolveBaseRequest resolves BaseRef without changing any checkout or ref.
type ResolveBaseRequest struct {
	RepositoryPath string
	BaseRef        string
}

// BaseRevision freezes one base ref at an exact commit in one canonical repository.
type BaseRevision struct {
	Repository Identity
	Ref        string
	CommitSHA  string
}

// BranchPlan is a closed choice between creating a new owned branch and
// reattaching a branch whose ownership was already recorded durably.
type BranchPlan interface {
	isBranchPlan()
	branchName() string
	expectedCommit() string
}

// CreateBranch creates Name at the frozen Base commit. Any existing ref is a collision.
type CreateBranch struct {
	Name string
	Base BaseRevision
}

func (CreateBranch) isBranchPlan()                {}
func (value CreateBranch) branchName() string     { return value.Name }
func (value CreateBranch) expectedCommit() string { return value.Base.CommitSHA }

// ReattachBranch attaches a previously owned branch only at its recorded tip.
type ReattachBranch struct {
	Name              string
	ExpectedCommitSHA string
}

func (ReattachBranch) isBranchPlan()                {}
func (value ReattachBranch) branchName() string     { return value.Name }
func (value ReattachBranch) expectedCommit() string { return value.ExpectedCommitSHA }

// AttachRequest creates or reconciles one worktree attachment. WorktreePath must
// be absolute and unused except for an exact idempotent attachment match.
type AttachRequest struct {
	RepositoryPath string
	WorktreePath   string
	OperationID    string
	Owner          Ownership
	Branch         BranchPlan
}

// RemoveRequest detaches one exact owned worktree. ExpectedHeadSHA and BranchName
// are required ownership evidence; Remove never deletes the branch.
type RemoveRequest struct {
	RepositoryPath  string
	WorktreePath    string
	OperationID     string
	Owner           Ownership
	BranchName      string
	ExpectedHeadSHA string
}

// Removal reports whether this call detached the worktree or reconciled an
// already-absent attachment.
type Removal struct {
	Repository    Identity
	WorktreePath  string
	AlreadyAbsent bool
}

// Checkout is a closed representation of a worktree's checked-out ref state.
type Checkout interface{ isCheckout() }

// BranchCheckout is a worktree attached to one local branch.
type BranchCheckout struct{ Name string }

func (BranchCheckout) isCheckout() {}

// DetachedCheckout is a non-bare worktree with detached HEAD.
type DetachedCheckout struct{}

func (DetachedCheckout) isCheckout() {}

// BareCheckout is the main entry for a bare repository.
type BareCheckout struct{}

func (BareCheckout) isCheckout() {}

// Condition is a closed representation of mutually exclusive workspace states.
type Condition interface{ isCondition() }

// Clean means the worktree has no tracked, untracked, conflicted, or in-progress state.
type Clean struct{}

func (Clean) isCondition() {}

// Dirty means ordinary tracked or untracked changes are present.
type Dirty struct{ Changes []string }

func (Dirty) isCondition() {}

// Conflicted means the index contains unmerged entries.
type Conflicted struct{ Changes []string }

func (Conflicted) isCondition() {}

// OperationInProgress means Git administrative state such as a merge or rebase is active.
type OperationInProgress struct {
	Operation string
	Changes   []string
}

func (OperationInProgress) isCondition() {}

// Unavailable means Git still registers a worktree whose filesystem state cannot
// be inspected, for example a prunable entry whose directory disappeared.
type Unavailable struct{ Reason string }

func (Unavailable) isCondition() {}

// LockState is a closed representation of Git's worktree lock state.
type LockState interface{ isLockState() }

// Unlocked means Git does not report the worktree as locked.
type Unlocked struct{}

func (Unlocked) isLockState() {}

// Locked means Git reports a worktree lock. Reason may be empty when Git has none.
type Locked struct{ Reason string }

func (Locked) isLockState() {}

// Worktree is a normalized observation. HeadSHA is meaningful for every non-bare
// registered worktree and Checkout carries the mutually exclusive ref state.
type Worktree struct {
	Path           string
	GitDir         string
	HeadSHA        string
	Checkout       Checkout
	Condition      Condition
	Lock           LockState
	PrunableReason string
}
