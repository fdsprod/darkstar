package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	port "darkstar/src/ports/workspace"
)

func (h *handle) ReplaceFile(ctx context.Context, area port.Area, name, expectedDigest string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validPath(name); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(expectedDigest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(expectedDigest) != expectedDigest {
		return errors.New("workspace replacement requires a lowercase SHA-256 digest")
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
	if err := plainPath(root, name); err != nil {
		return err
	}
	previous, err := root.Open(name)
	if err != nil {
		return err
	}
	hash := sha256.New()
	oldSize, readErr := io.Copy(hash, io.LimitReader(previous, h.owner.limits.FileBytes+1))
	closeErr := previous.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if oldSize > h.owner.limits.FileBytes {
		return errors.New("workspace file exceeds byte limit")
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return errors.New("workspace file revision conflict")
	}
	var used int64
	err = fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, walkErr error) error {
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
		used += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	if used-oldSize+int64(len(data)) > h.owner.limits.AreaBytes {
		return errors.New("workspace area quota exceeded")
	}
	// Open a directory handle once. Temp creation and rename are relative to
	// this anchored directory, so changing parent path aliases cannot redirect
	// replacement. Native rename fallbacks retain Go 1.24 support.
	parent, err := root.OpenRoot(path.Dir(name))
	if err != nil {
		return err
	}
	defer parent.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := fmt.Sprintf(".workspace-replace-%x", nonce)
	file, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { file.Close(); _ = parent.Remove(temporary) }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return renameFile(parent, temporary, path.Base(name))
}
