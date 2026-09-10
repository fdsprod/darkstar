package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"darkstar/src/ports/repository"
)

// RelocateWorktree moves a registered linked worktree without changing its
// branch, index, files, or commits. A completed move can be safely reconciled.
func (manager *Manager) RelocateWorktree(ctx context.Context, root, from, to, branch, head string) error {
	if !filepath.IsAbs(from) || !filepath.IsAbs(to) || pathsEqual(from, root) || pathsEqual(to, root) || pathsEqual(from, to) {
		return errors.New("relocation requires distinct absolute linked worktree paths")
	}
	observation, err := manager.Inspect(ctx, repository.InspectRequest{Path: root})
	if err != nil {
		return err
	}
	var source, destination *repository.Worktree
	for i := range observation.Worktrees {
		tree := &observation.Worktrees[i]
		if pathsEqual(tree.Path, from) {
			source = tree
		}
		if pathsEqual(tree.Path, to) {
			destination = tree
		}
	}
	if source == nil && destination != nil && relocationMatches(*destination, branch, head) {
		return nil
	}
	if source == nil || !relocationMatches(*source, branch, head) || destination != nil {
		return errors.New("worktree relocation does not match recorded branch and HEAD")
	}
	if _, err = os.Lstat(to); err == nil {
		return errors.New("worktree relocation destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	resolvedFrom, err := filepath.EvalSymlinks(from)
	if err != nil {
		return err
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(to))
	if err != nil {
		return err
	}
	if !pathsEqual(resolvedFrom, from) || !pathsEqual(filepath.Join(resolvedParent, filepath.Base(to)), to) {
		return errors.New("worktree relocation paths must not redirect through symlinks")
	}
	if _, err = manager.run(ctx, root, "worktree", "move", "--", from, to); err != nil {
		return err
	}
	after, err := manager.Inspect(ctx, repository.InspectRequest{Path: root})
	if err != nil {
		return err
	}
	for _, tree := range after.Worktrees {
		if pathsEqual(tree.Path, to) && relocationMatches(tree, branch, head) {
			return nil
		}
	}
	return errors.New("moved worktree could not be verified")
}

func relocationMatches(tree repository.Worktree, branch, head string) bool {
	_, unlocked := tree.Lock.(repository.Unlocked)
	return checkoutBranch(tree.Checkout) == branch && tree.HeadSHA == head && tree.PrunableReason == "" && unlocked
}
