package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	providerport "darkstar/src/ports/provider"
)

// EvidenceRecord is one immutable provider observation to persist.
type EvidenceRecord struct {
	AttemptID string
	Sequence  uint64
	Kind      string
	MediaType string
	Data      []byte
}

// EvidenceRecorder persists raw provider observations before they are exposed
// as durable event references.
type EvidenceRecorder interface {
	Record(context.Context, EvidenceRecord) (providerport.Evidence, error)
}

// DirectoryEvidenceRecorder stores one atomic, content-addressed observation
// file per event beneath a configured DARKSTAR-owned root.
type DirectoryEvidenceRecorder struct{ root string }

func NewDirectoryEvidenceRecorder(root string) (*DirectoryEvidenceRecorder, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("codex evidence root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex evidence root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create Codex evidence root: %w", err)
	}
	return &DirectoryEvidenceRecorder{root: filepath.Clean(absolute)}, nil
}

func (recorder *DirectoryEvidenceRecorder) Record(ctx context.Context, record EvidenceRecord) (providerport.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return providerport.Evidence{}, err
	}
	if strings.TrimSpace(record.AttemptID) == "" || record.Sequence == 0 || strings.TrimSpace(record.Kind) == "" || len(record.Data) == 0 {
		return providerport.Evidence{}, errors.New("codex evidence record requires attempt, sequence, kind, and data")
	}
	digestBytes := sha256.Sum256(record.Data)
	digest := hex.EncodeToString(digestBytes[:])
	directory := filepath.Join(recorder.root, safePathPart(record.AttemptID))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return providerport.Evidence{}, fmt.Errorf("create Codex attempt evidence directory: %w", err)
	}
	name := fmt.Sprintf("%020d-%s-%s.json", record.Sequence, safePathPart(record.Kind), digest[:16])
	target := filepath.Join(directory, name)
	temporary, err := os.CreateTemp(directory, ".record-*.tmp")
	if err != nil {
		return providerport.Evidence{}, fmt.Errorf("create Codex evidence temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return providerport.Evidence{}, fmt.Errorf("protect Codex evidence temporary file: %w", err)
	}
	if _, err := temporary.Write(record.Data); err != nil {
		return providerport.Evidence{}, fmt.Errorf("write Codex evidence: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return providerport.Evidence{}, fmt.Errorf("sync Codex evidence: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return providerport.Evidence{}, fmt.Errorf("close Codex evidence: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return providerport.Evidence{}, fmt.Errorf("commit Codex evidence: %w", err)
	}
	committed = true
	return providerport.Evidence{Kind: record.Kind, Ref: target, Digest: digest}, nil
}

func safePathPart(value string) string {
	value = strings.TrimSpace(value)
	var result strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' || character == '.' {
			result.WriteRune(character)
		} else {
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 || result.String() == "." || result.String() == ".." {
		return "unknown"
	}
	return result.String()
}
