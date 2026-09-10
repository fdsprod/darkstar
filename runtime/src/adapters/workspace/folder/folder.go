// Package workspace implements private folder-backed work-item storage.
package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	port "darkstar/src/ports/workspace"
)

// Limits bound individual files and each granted area. Zero selects defaults.
type Limits struct {
	FileBytes int64
	AreaBytes int64
	AreaFiles int
}

type Folder struct {
	root   *os.Root
	limits Limits
	mu     sync.Mutex
}

// New opens a host-owned root. The caller must not expose this directory as an
// agent mount. All plugin access goes through bound handles. os.Root provides
// traversal-resistant operations, including Windows junction containment.
func New(directory string, limits Limits) (*Folder, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("workspace root must be absolute")
	}
	if limits.FileBytes == 0 {
		limits.FileBytes = 25 << 20
	}
	if limits.AreaBytes == 0 {
		limits.AreaBytes = 100 << 20
	}
	if limits.AreaFiles == 0 {
		limits.AreaFiles = 1000
	}
	if limits.FileBytes < 0 || limits.AreaBytes < limits.FileBytes || limits.AreaFiles < 1 {
		return nil, errors.New("invalid workspace limits")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &Folder{root: root, limits: limits}, nil
}

func (f *Folder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.root.Close()
}

func identity(value string) (string, error) {
	if strings.TrimSpace(value) == "" || len(value) > 1024 {
		return "", errors.New("workspace owner identity is required and must be bounded")
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value))), nil
}

func (f *Folder) Ensure(ctx context.Context, workID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := identity(workID)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return directories(f.root, id)
}

func (f *Folder) Bind(ctx context.Context, grant port.Grant) (port.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	work, err := identity(grant.WorkItemID)
	if err != nil {
		return nil, err
	}
	plugin, err := identity(grant.PluginID)
	if err != nil {
		return nil, err
	}
	attempt, err := identity(grant.AttemptID)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	h := &handle{owner: f, roots: map[port.Area]*os.Root{}}
	paths := map[port.Area]string{
		port.PluginState: path.Join(work, "plugins", plugin, "state"),
		port.Scratch:     path.Join(work, "plugins", plugin, "attempts", attempt, "scratch"),
		port.Staged:      path.Join(work, "plugins", plugin, "attempts", attempt, "staged"),
	}
	for area, name := range paths {
		if err := directories(f.root, name); err != nil {
			h.close()
			return nil, err
		}
		root, err := f.root.OpenRoot(name)
		if err != nil {
			h.close()
			return nil, err
		}
		h.roots[area] = root
	}
	return h, nil
}

// directories uses only Go 1.24 Root operations. Reject links even when they
// remain inside the root, so one namespace cannot alias another namespace.
func directories(root *os.Root, name string) error {
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("workspace directory is not a plain directory")
		}
	}
	return nil
}

func validPath(name string) error {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:") || len(name) > 1024 {
		return errors.New("workspace path must be a bounded relative file path")
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("workspace path has an ambiguous component")
		}
	}
	return nil
}

type handle struct {
	owner  *Folder
	roots  map[port.Area]*os.Root
	closed bool
}

func (h *handle) area(area port.Area) (*os.Root, error) {
	if h.closed {
		return nil, errors.New("workspace handle is closed")
	}
	root, ok := h.roots[area]
	if !ok {
		return nil, errors.New("unknown workspace area")
	}
	return root, nil
}

func (h *handle) ReadFile(ctx context.Context, area port.Area, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validPath(name); err != nil {
		return nil, err
	}
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()
	root, err := h.area(area)
	if err != nil {
		return nil, err
	}
	if err := plainPath(root, name); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("workspace reads require a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, h.owner.limits.FileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > h.owner.limits.FileBytes {
		return nil, errors.New("workspace file exceeds byte limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func plainPath(root *os.Root, name string) error {
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("workspace symbolic links are not permitted")
		}
		if current == name && !info.Mode().IsRegular() {
			return errors.New("workspace reads require a regular file")
		}
	}
	return nil
}

func (h *handle) WriteFile(ctx context.Context, area port.Area, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validPath(name); err != nil {
		return err
	}
	if int64(len(data)) > h.owner.limits.FileBytes {
		return errors.New("workspace file exceeds byte limit")
	}
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()
	root, err := h.area(area)
	if err != nil {
		return err
	}
	var bytes int64
	var files int
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("workspace symbolic links are not permitted")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("workspace contains a non-regular file")
		}
		bytes += info.Size()
		files++
		return nil
	})
	if err != nil {
		return err
	}
	if bytes+int64(len(data)) > h.owner.limits.AreaBytes || files >= h.owner.limits.AreaFiles {
		return errors.New("workspace area quota exceeded")
	}
	if parent := path.Dir(name); parent != "." {
		if err := directories(root, parent); err != nil {
			return err
		}
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	// Exclusivity prevents retries or aliases from replacing an existing version.
	// Failed writes are removed only from the newly created, private path.
	ok := false
	defer func() {
		file.Close()
		if !ok {
			_ = root.Remove(name)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func (h *handle) close() error {
	var result error
	for _, root := range h.roots {
		result = errors.Join(result, root.Close())
	}
	h.closed = true
	return result
}
func (h *handle) Close() error {
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()
	if h.closed {
		return nil
	}
	return h.close()
}

var _ port.Manager = (*Folder)(nil)
