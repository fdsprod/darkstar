// Package localconnection retains source evidence and resolves protected local
// credential references. Neither store grants tracker or execution authority.
package localconnection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"darkstar/src/ports"
	"darkstar/src/ports/trackerconnection"
)

// Provider bodies are bounded at 8 MiB; provenance envelopes can retain both
// parsed JSON and original bytes (base64), so the complete envelope needs 32 MiB.
const maxEvidenceBytes = 32 << 20

type EvidenceStore struct {
	root string
}

var _ trackerconnection.EvidenceStore = (*EvidenceStore)(nil)

func NewEvidenceStore(root string) (*EvidenceStore, error) {
	if !filepath.IsAbs(root) {
		return nil, safeFailure(ports.FailureInvalidRequest, "source evidence requires an absolute storage directory")
	}
	return &EvidenceStore{root: filepath.Clean(root)}, nil
}

func (s *EvidenceStore) Retain(ctx context.Context, content []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(content) == 0 || len(content) > maxEvidenceBytes {
		return "", safeFailure(ports.FailureInvalidRequest, "source evidence is empty or exceeds its size limit")
	}
	digest := sha256.Sum256(content)
	name := hex.EncodeToString(digest[:])
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", safeFailure(ports.FailureUnavailable, "source evidence storage is unavailable")
	}
	file, err := os.CreateTemp(s.root, ".evidence-")
	if err != nil {
		return "", safeFailure(ports.FailureUnavailable, "source evidence storage is unavailable")
	}
	temporary := file.Name()
	defer func() {
		_ = os.Remove(temporary)
	}()
	if err := protectStoredFile(file); err != nil {
		_ = file.Close()
		return "", safeFailure(ports.FailureUnavailable, "source evidence storage could not be protected")
	}
	if err := writeAndClose(file, content); err != nil {
		return "", safeFailure(ports.FailureUnavailable, "source evidence could not be retained")
	}
	// Linking publishes complete bytes without replacing an existing revision.
	if err := os.Link(temporary, filepath.Join(s.root, name)); err != nil && !errors.Is(err, os.ErrExist) {
		return "", safeFailure(ports.FailureUnavailable, "source evidence could not be published")
	}
	if err := syncStorageDirectory(s.root); err != nil {
		return "", safeFailure(ports.FailureUnavailable, "source evidence publication could not be synchronized")
	}
	ref := "tracker-evidence:sha256:" + name
	if _, err := s.Read(ctx, ref); err != nil {
		return "", err
	}
	return ref, nil
}

func (s *EvidenceStore) Read(ctx context.Context, ref string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(ref, "tracker-evidence:sha256:")
	decoded, err := hex.DecodeString(name)
	if !strings.HasPrefix(ref, "tracker-evidence:sha256:") || err != nil || len(decoded) != sha256.Size || name != strings.ToLower(name) {
		return nil, safeFailure(ports.FailureInvalidRequest, "invalid source evidence reference")
	}
	content, err := boundedRead(filepath.Join(s.root, name), maxEvidenceBytes)
	if err != nil {
		return nil, safeFailure(ports.FailureUnavailable, "retained source evidence is unavailable")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != name {
		return nil, safeFailure(ports.FailureConflict, "retained source evidence failed its integrity check")
	}
	return content, nil
}

type Credentials struct {
	root string
}

var _ trackerconnection.CredentialResolver = (*Credentials)(nil)

func NewCredentials(root string) (*Credentials, error) {
	if !filepath.IsAbs(root) {
		return nil, safeFailure(ports.FailureInvalidRequest, "credentials require an absolute storage directory")
	}
	return &Credentials{root: filepath.Clean(root)}, nil
}

func credentialName(ref string) (string, error) {
	if ref == "" || len(ref) > 256 || strings.TrimSpace(ref) != ref || strings.ContainsAny(ref, "\r\n\x00") {
		return "", safeFailure(ports.FailureInvalidRequest, "invalid credential reference")
	}
	digest := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(digest[:]) + ".credential", nil
}

// Put rotates only the named secret. Configuration retains the reference, never
// the value; Windows encrypts with the current user's DPAPI identity.
func (s *Credentials) Put(ctx context.Context, ref, secret string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := credentialName(ref)
	if err != nil {
		return err
	}
	if !validSecret(secret) {
		return safeFailure(ports.FailureInvalidRequest, "credential is empty or invalid")
	}
	protected, err := protectSecret([]byte(secret))
	if err != nil {
		return safeFailure(ports.FailureUnavailable, "credential protection is unavailable")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return safeFailure(ports.FailureUnavailable, "credential storage is unavailable")
	}
	file, err := os.CreateTemp(s.root, ".credential-")
	if err != nil {
		return safeFailure(ports.FailureUnavailable, "credential storage is unavailable")
	}
	temporary := file.Name()
	defer func() {
		_ = os.Remove(temporary)
	}()
	if err := protectStoredFile(file); err != nil {
		_ = file.Close()
		return safeFailure(ports.FailureUnavailable, "credential storage could not be protected")
	}
	if err := writeAndClose(file, protected); err != nil {
		return safeFailure(ports.FailureUnavailable, "credential could not be stored")
	}
	if err := replacePublishedFile(temporary, filepath.Join(s.root, name)); err != nil {
		return safeFailure(ports.FailureUnavailable, "credential could not be replaced")
	}
	if err := syncStorageDirectory(s.root); err != nil {
		return safeFailure(ports.FailureUnavailable, "credential replacement could not be synchronized")
	}
	return nil
}

func (s *Credentials) Resolve(ctx context.Context, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, err := credentialName(ref)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.root, name)
	content, err := boundedRead(path, 64<<10)
	if err != nil {
		return "", safeFailure(ports.FailureUnauthenticated, "protected credential is unavailable")
	}
	secret, err := unprotectSecret(content)
	if err != nil || !validSecret(string(secret)) {
		return "", safeFailure(ports.FailureUnauthenticated, "protected credential could not be unlocked")
	}
	return string(secret), nil
}

func validSecret(secret string) bool {
	return secret != "" && len(secret) <= 8192 && strings.TrimSpace(secret) == secret && !strings.ContainsAny(secret, "\r\n\x00")
}

func writeAndClose(file *os.File, content []byte) error {
	_, err := file.Write(content)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func boundedRead(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("invalid stored file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || !os.SameFile(before, info) {
		return nil, errors.New("invalid stored file")
	}
	// Check protection on the same handle that supplies bytes; a pathname check
	// followed by reopening would allow replacement between verification and read.
	if err := verifyStoredFile(file); err != nil {
		return nil, errors.New("stored file is not protected")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if len(content) > int(limit) {
		return nil, errors.New("stored file exceeds limit")
	}
	return content, err
}

func safeFailure(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}
