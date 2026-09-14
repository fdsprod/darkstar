// Package git materializes immutable committed evidence without checking out files.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"darkstar/src/platform/process"
	"darkstar/src/ports/repositorysnapshot"
)

var (
	ErrInvalid     = errors.New("invalid repository snapshot request")
	ErrUnavailable = errors.New("repository snapshot source is unavailable")
	ErrCorrupt     = errors.New("retained repository snapshot failed integrity verification")
	ErrLimit       = errors.New("repository snapshot exceeds configured limits")
)

type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxTreeBytes  int64
}

type Adapter struct {
	root       string
	executable string
	limits     Limits
}

var _ repositorysnapshot.Exporter = (*Adapter)(nil)

func New(root string, limits Limits) (*Adapter, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: snapshot storage root must be absolute", ErrInvalid)
	}
	if limits.MaxFiles < 0 || limits.MaxFileBytes < 0 || limits.MaxTotalBytes < 0 || limits.MaxTreeBytes < 0 {
		return nil, fmt.Errorf("%w: limits must be positive", ErrInvalid)
	}
	if limits.MaxFiles == 0 {
		limits.MaxFiles = 20000
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = 8 << 20
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = 64 << 20
	}
	if limits.MaxTreeBytes == 0 {
		limits.MaxTreeBytes = 16 << 20
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	executable, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	return &Adapter{root: filepath.Clean(canonical), executable: executable, limits: limits}, nil
}

func (a *Adapter) Resolve(ctx context.Context, request repositorysnapshot.ResolveRequest) (repositorysnapshot.ResolvedRevision, error) {
	if strings.TrimSpace(request.RepositoryID) == "" || strings.TrimSpace(request.Ref) != request.Ref || request.Ref == "" || strings.HasPrefix(request.Ref, "-") || strings.ContainsAny(request.Ref, "\x00\r\n") {
		return repositorysnapshot.ResolvedRevision{}, fmt.Errorf("%w: a repository and explicit ref are required", ErrInvalid)
	}
	if err := a.verifySource(ctx, request.Root, request.CommonGitDir); err != nil {
		return repositorysnapshot.ResolvedRevision{}, err
	}
	commit, err := a.git(ctx, request.Root, 256, "rev-parse", "--verify", "--end-of-options", request.Ref+"^{commit}")
	if err != nil {
		return repositorysnapshot.ResolvedRevision{}, fmt.Errorf("%w: selected ref %q does not resolve to an available commit: %v", ErrUnavailable, request.Ref, err)
	}
	sha := strings.TrimSpace(string(commit))
	if !objectID(sha) {
		return repositorysnapshot.ResolvedRevision{}, fmt.Errorf("%w: invalid commit identity", ErrUnavailable)
	}
	tree, err := a.git(ctx, request.Root, 256, "rev-parse", "--verify", "--end-of-options", sha+"^{tree}")
	if err != nil || !objectID(strings.TrimSpace(string(tree))) {
		return repositorysnapshot.ResolvedRevision{}, fmt.Errorf("%w: selected commit tree is unavailable", ErrUnavailable)
	}
	return repositorysnapshot.ResolvedRevision{CommitSHA: sha, TreeSHA: strings.TrimSpace(string(tree))}, nil
}

func (a *Adapter) Materialize(ctx context.Context, request repositorysnapshot.ExportRequest) (repositorysnapshot.Evidence, error) {
	request, key, err := normalizeRequest(request)
	if err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	if err := a.checkStorage(); err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	destination := filepath.Join(a.root, key)
	if _, err := os.Lstat(destination); err == nil {
		return a.readEvidence(ctx, key)
	} else if !errors.Is(err, os.ErrNotExist) {
		return repositorysnapshot.Evidence{}, err
	}
	// Reuse above needs no source checkout. A missing cache can only be recreated
	// from the exact recorded commit; no ref, checkout, or HEAD is substituted.
	if err := a.verifySource(ctx, request.Root, request.CommonGitDir); err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	kind, err := a.git(ctx, request.Root, 64, "cat-file", "-t", request.CommitSHA)
	if err != nil || strings.TrimSpace(string(kind)) != "commit" {
		return repositorysnapshot.Evidence{}, fmt.Errorf("%w: recorded commit %s is unavailable", ErrUnavailable, request.CommitSHA)
	}
	staging, err := os.MkdirTemp(a.root, ".prepare-")
	if err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	defer func() {
		// staging is a fresh direct child created by MkdirTemp above.
		_ = os.RemoveAll(staging)
	}()
	content := filepath.Join(staging, "content")
	if err := os.Mkdir(content, 0700); err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	manifest, err := a.export(ctx, request, content)
	if err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	if err := writeExclusive(filepath.Join(staging, "manifest.json"), raw); err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	if err := a.verifyFiles(ctx, content, manifest); err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		// Concurrent publication of the same immutable request is idempotent only
		// when the winner verifies. Never replace an existing evidence directory.
		if _, existsErr := os.Lstat(destination); existsErr == nil {
			return a.readEvidence(ctx, key)
		}
		return repositorysnapshot.Evidence{}, err
	}
	return a.readEvidence(ctx, key)
}

func (a *Adapter) Verify(ctx context.Context, evidence repositorysnapshot.Evidence) error {
	if !objectDigest(evidence.CacheKey) || !samePath(evidence.Root, filepath.Join(a.root, evidence.CacheKey, "content")) {
		return fmt.Errorf("%w: evidence root is outside snapshot storage", ErrCorrupt)
	}
	actual, err := a.readEvidence(ctx, evidence.CacheKey)
	if err != nil {
		return err
	}
	left, _ := json.Marshal(actual)
	right, _ := json.Marshal(evidence)
	if !bytes.Equal(left, right) {
		return fmt.Errorf("%w: descriptor differs from the retained manifest", ErrCorrupt)
	}
	return nil
}

func normalizeRequest(request repositorysnapshot.ExportRequest) (repositorysnapshot.ExportRequest, string, error) {
	if strings.TrimSpace(request.ScopeID) == "" || strings.TrimSpace(request.RepositoryID) == "" || !filepath.IsAbs(request.Root) || !filepath.IsAbs(request.CommonGitDir) || !objectID(request.CommitSHA) {
		return request, "", fmt.Errorf("%w: frozen scope, repository coordinates, and exact commit are required", ErrInvalid)
	}
	request.Root = filepath.Clean(request.Root)
	request.CommonGitDir = filepath.Clean(request.CommonGitDir)
	if request.PathScope != nil {
		request.PathScope = append([]string{}, request.PathScope...)
		for index, path := range request.PathScope {
			if path == "" || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") {
				return request, "", fmt.Errorf("%w: path scope must be repository-relative", ErrInvalid)
			}
			clean := pathpkg.Clean(path)
			if clean != "." {
				if err := portablePath(clean); err != nil {
					return request, "", fmt.Errorf("%w: path scope: %v", ErrInvalid, err)
				}
			}
			request.PathScope[index] = clean
		}
		sort.Strings(request.PathScope)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return request, "", err
	}
	return request, digest(raw), nil
}

func (a *Adapter) verifySource(ctx context.Context, root, common string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(common) {
		return fmt.Errorf("%w: canonical absolute repository coordinates are required", ErrInvalid)
	}
	for _, coordinate := range []struct{ path, argument string }{{root, "--show-toplevel"}, {common, "--git-common-dir"}} {
		output, err := a.git(ctx, root, 32768, "rev-parse", "--path-format=absolute", coordinate.argument)
		if err != nil {
			return fmt.Errorf("%w: repository coordinates cannot be inspected: %v", ErrUnavailable, err)
		}
		canonical, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
		if err != nil || !samePath(canonical, coordinate.path) {
			return fmt.Errorf("%w: registered repository coordinates changed or cannot be verified", ErrUnavailable)
		}
	}
	return nil
}

func (a *Adapter) git(ctx context.Context, root string, limit int64, arguments ...string) ([]byte, error) {
	return a.gitInput(ctx, root, limit, nil, arguments...)
}

func (a *Adapter) gitInput(ctx context.Context, root string, limit int64, input []byte, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	prefix := []string{"--no-optional-locks", "--no-replace-objects", "-c", "protocol.allow=never", "-c", "core.fsmonitor=false"}
	command := exec.CommandContext(ctx, a.executable, append(prefix, arguments...)...)
	command.Dir = root
	command.Stdin = bytes.NewReader(input)
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(variable), "GIT_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_NOSYSTEM=1", "GCM_INTERACTIVE=Never")
	process.HideConsole(command)
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: 8192}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	if stdout.exceeded {
		return nil, ErrLimit
	}
	if err != nil {
		return nil, fmt.Errorf("git object read failed: %w", err)
	}
	return stdout.Bytes(), nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if int64(b.buffer.Len())+int64(len(value)) > b.limit {
		b.exceeded = true
		return 0, ErrLimit
	}
	return b.buffer.Write(value)
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func writeExclusive(path string, value []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func digest(value []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func objectDigest(value string) bool {
	return len(value) == 64 && objectID(value)
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: expected a regular evidence file", ErrCorrupt)
	}
	if info.Size() > limit {
		return nil, ErrLimit
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%w: evidence file changed while opening", ErrCorrupt)
	}
	value, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		return nil, ErrLimit
	}
	return value, nil
}
