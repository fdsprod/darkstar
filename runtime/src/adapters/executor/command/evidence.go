package command

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

	executorport "darkstar/src/ports/executor"
)

// DirectoryEvidenceRecorder stores atomic, content-addressed command evidence
// beneath a DARKSTAR-owned root.
type DirectoryEvidenceRecorder struct{ root string }

func NewDirectoryEvidenceRecorder(root string) (*DirectoryEvidenceRecorder, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("command evidence root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve command evidence root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create command evidence root: %w", err)
	}
	return &DirectoryEvidenceRecorder{root: filepath.Clean(absolute)}, nil
}

func (recorder *DirectoryEvidenceRecorder) Record(ctx context.Context, record EvidenceRecord) (executorport.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return executorport.Evidence{}, err
	}
	if strings.TrimSpace(record.AttemptID) == "" || strings.TrimSpace(record.Kind) == "" || len(record.Data) == 0 {
		return executorport.Evidence{}, errors.New("command evidence requires attempt, kind, and data")
	}
	digestBytes := sha256.Sum256(record.Data)
	digest := hex.EncodeToString(digestBytes[:])
	directory := filepath.Join(recorder.root, safePathPart(record.AttemptID))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return executorport.Evidence{}, fmt.Errorf("create command evidence directory: %w", err)
	}
	target := filepath.Join(directory, safePathPart(record.Kind)+"-"+digest[:16]+".json")
	if existing, err := os.ReadFile(target); err == nil {
		existingDigest := sha256.Sum256(existing)
		if existingDigest == digestBytes {
			return executorport.Evidence{Kind: record.Kind, Ref: target, Digest: digest}, nil
		}
		return executorport.Evidence{}, errors.New("command evidence path has conflicting content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return executorport.Evidence{}, fmt.Errorf("inspect command evidence: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".record-*.tmp")
	if err != nil {
		return executorport.Evidence{}, fmt.Errorf("create command evidence temporary file: %w", err)
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
		return executorport.Evidence{}, fmt.Errorf("protect command evidence temporary file: %w", err)
	}
	if _, err := temporary.Write(record.Data); err != nil {
		return executorport.Evidence{}, fmt.Errorf("write command evidence: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return executorport.Evidence{}, fmt.Errorf("sync command evidence: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return executorport.Evidence{}, fmt.Errorf("close command evidence: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		if existing, readErr := os.ReadFile(target); readErr == nil {
			existingDigest := sha256.Sum256(existing)
			if existingDigest == digestBytes {
				return executorport.Evidence{Kind: record.Kind, Ref: target, Digest: digest}, nil
			}
		}
		return executorport.Evidence{}, fmt.Errorf("commit command evidence: %w", err)
	}
	committed = true
	return executorport.Evidence{Kind: record.Kind, Ref: target, Digest: digest}, nil
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
