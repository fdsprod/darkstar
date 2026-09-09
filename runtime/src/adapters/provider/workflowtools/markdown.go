package workflowtools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// PrepareMarkdown records a private baseline, so pre-existing documents are not
// presented as produced artifacts. History is stored outside the agent checkout.
func (s *Session) PrepareMarkdown(ctx context.Context) error {
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS markdown_observations(run_id TEXT NOT NULL,attempt_id TEXT NOT NULL,snapshot TEXT NOT NULL,PRIMARY KEY(run_id,attempt_id))`)
	if err != nil {
		return err
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM markdown_observations WHERE run_id=? AND attempt_id=?`, s.RunID, s.AttemptID).Scan(&count); err != nil || count > 0 {
		return err
	}
	values, err := s.markdownFiles(ctx)
	if err != nil {
		return err
	}
	snapshot, _ := json.Marshal(markdownDigests(values))
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO markdown_observations VALUES(?,?,?)`, s.RunID, s.AttemptID, string(snapshot))
	return err
}
func (s *Session) CaptureMarkdown(ctx context.Context, key string) error {
	if s.Workspace == "" {
		return nil
	}
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	var raw string
	err = db.QueryRowContext(ctx, `SELECT snapshot FROM markdown_observations WHERE run_id=? AND attempt_id=?`, s.RunID, s.AttemptID).Scan(&raw)
	db.Close()
	if err != nil {
		return err
	}
	before := map[string]string{}
	if err = json.Unmarshal([]byte(raw), &before); err != nil {
		return err
	}
	values, err := s.markdownFiles(ctx)
	if err != nil {
		return err
	}
	after := markdownDigests(values)
	for name, content := range values {
		if before[name] == after[name] {
			continue
		}
		encoded, _ := json.Marshal(content)
		if _, err = s.append(ctx, "output:workspace:"+name, "snapshot", name, "snapshot:"+s.AttemptID+":"+key+":"+name+":"+after[name], string(encoded), false); err != nil {
			return err
		}
	}
	snapshot, _ := json.Marshal(after)
	db, err = s.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `UPDATE markdown_observations SET snapshot=? WHERE run_id=? AND attempt_id=?`, string(snapshot), s.RunID, s.AttemptID)
	return err
}
func markdownDigests(values map[string]string) map[string]string {
	digests := map[string]string{}
	for name, content := range values {
		digests[name] = fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	}
	return digests
}
func (s *Session) markdownFiles(ctx context.Context) (map[string]string, error) {
	if !filepath.IsAbs(s.Workspace) {
		return nil, errors.New("Markdown capture requires an absolute workspace")
	}
	command := exec.CommandContext(ctx, "git", "-C", s.Workspace, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*.md", "*.markdown")
	paths, err := command.Output()
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(s.Workspace)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, name := range strings.Split(string(paths), "\x00") {
		if name == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, errors.New("invalid Markdown workspace path")
		}
		path := filepath.Join(root, clean)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return nil, err
		}
		if utf8.Valid(raw) {
			values[filepath.ToSlash(clean)] = string(raw)
		}
	}
	return values, nil
}
