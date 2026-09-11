package workflowtools

import (
	"context"
	"crypto/sha256"
	"darkstar/src/core/nodes"
	"darkstar/src/platform/process"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// PrepareWorkspace retains an attempt's original baseline across provider
// reconnects. It lives in daemon-owned storage, never in the agent workspace.
func (s *Session) PrepareWorkspace(ctx context.Context) error {
	if !filepath.IsAbs(s.Workspace) {
		return errors.New("implementation requires an absolute repository workspace")
	}
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS implementation_baselines(run_id TEXT NOT NULL,attempt_id TEXT NOT NULL,workspace TEXT NOT NULL,digests TEXT NOT NULL,PRIMARY KEY(run_id,attempt_id))`)
	if err != nil {
		return err
	}
	var exists int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM implementation_baselines WHERE run_id=? AND attempt_id=?`, s.RunID, s.AttemptID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		_, err = s.baseline(ctx)
		return err
	}
	snapshot, err := workspaceSnapshot(ctx, s.Workspace)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO implementation_baselines(run_id,attempt_id,workspace,digests) VALUES(?,?,?,?)`, s.RunID, s.AttemptID, s.Workspace, string(raw))
	return err
}

func (s *Session) baseline(ctx context.Context) (map[string]string, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var workspace, raw string
	if err = db.QueryRowContext(ctx, `SELECT workspace,digests FROM implementation_baselines WHERE run_id=? AND attempt_id=?`, s.RunID, s.AttemptID).Scan(&workspace, &raw); err != nil {
		return nil, err
	}
	if filepath.Clean(workspace) != filepath.Clean(s.Workspace) {
		return nil, errors.New("implementation workspace differs from the attempt baseline")
	}
	var result map[string]string
	err = json.Unmarshal([]byte(raw), &result)
	return result, err
}

func workspaceSnapshot(ctx context.Context, workspace string) (map[string]string, error) {
	// Git supplies tracked and nonignored untracked paths, including files that
	// were already dirty before this attempt. No reset, checkout, or clean occurs.
	command := exec.CommandContext(ctx, "git", "-C", workspace, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	process.HideConsole(command)
	paths, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("implementation needs a readable Git repository: %w", err)
	}
	result := map[string]string{}
	for _, name := range strings.Split(string(paths), "\x00") {
		if name == "" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, errors.New("repository returned a path outside its workspace")
		}
		path := filepath.Join(workspace, clean)
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		hash := sha256.New()
		if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return nil, linkErr
			}
			_, _ = io.WriteString(hash, "symlink:"+target)
		} else if info.Mode().IsRegular() {
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return nil, resolveErr
			}
			root, resolveErr := filepath.EvalSymlinks(workspace)
			if resolveErr != nil {
				return nil, resolveErr
			}
			relative, resolveErr := filepath.Rel(root, resolved)
			if resolveErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, errors.New("implementation evidence cannot follow files outside the workspace")
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return nil, openErr
			}
			_, copyErr := io.Copy(hash, file)
			_ = file.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			_, _ = fmt.Fprintf(hash, ";mode:%d", info.Mode().Perm())
		} else {
			continue
		}
		result[filepath.ToSlash(clean)] = hex.EncodeToString(hash.Sum(nil))
	}
	return result, nil
}

func (s *Session) workspaceChanges(ctx context.Context) ([]string, error) {
	before, err := s.baseline(ctx)
	if err != nil {
		return nil, err
	}
	after, err := workspaceSnapshot(ctx, s.Workspace)
	if err != nil {
		return nil, err
	}
	changes := []string{}
	for path, digest := range after {
		if before[path] != digest {
			changes = append(changes, path)
		}
	}
	for path := range before {
		if _, exists := after[path]; !exists {
			changes = append(changes, path)
		}
	}
	slices.Sort(changes)
	return changes, nil
}

func (s *Session) validateWorkspaceResult(ctx context.Context, raw json.RawMessage) error {
	if err := nodes.ValidateImplementationResult(ctx, raw, s.workspaceChanges); err != nil {
		return err
	}
	var sealed struct {
		SnapshotDigest string `json:"snapshotDigest"`
	}
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return err
	}
	if sealed.SnapshotDigest != "" {
		current, err := WorkspaceDigest(ctx, s.Workspace)
		if err != nil {
			return err
		}
		if sealed.SnapshotDigest != current {
			return errors.New("workspace changed after changeset submission; submit a fresh changeset")
		}
	}
	return nil
}

// WorkspaceDigest binds delivery text to exact file content without another
// model reading the diff. This is evidence, not authorization to commit or push.
func WorkspaceDigest(ctx context.Context, workspace string) (string, error) {
	snapshot, err := workspaceSnapshot(ctx, workspace)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Session) sealChangeset(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	digest, err := WorkspaceDigest(ctx, s.Workspace)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	fields["snapshotDigest"], _ = json.Marshal(digest)
	return json.Marshal(fields)
}
