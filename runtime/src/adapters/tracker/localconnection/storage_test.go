package localconnection

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestEvidenceRetainsOriginalVersionsAndDetectsTampering(t *testing.T) {
	ctx := context.Background()
	store, err := NewEvidenceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"description":"original untrusted provider content"}`)
	first, err := store.Retain(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.Retain(ctx, original)
	if err != nil || first != repeated {
		t.Fatalf("duplicate evidence was not idempotent: %v", err)
	}
	second, err := store.Retain(ctx, []byte(`{"description":"changed content"}`))
	if err != nil || second == first {
		t.Fatalf("changed evidence did not create a new version: %v", err)
	}
	retained, err := store.Read(ctx, first)
	if err != nil || !bytes.Equal(retained, original) {
		t.Fatalf("original was not retained: %v", err)
	}
	if _, err := store.Read(ctx, "tracker-evidence:sha256:../../secret"); err == nil {
		t.Fatal("accepted path traversal evidence reference")
	}
	path := filepath.Join(store.root, strings.TrimPrefix(first, "tracker-evidence:sha256:"))
	if err := os.WriteFile(path, []byte("corruption"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, first); err == nil {
		t.Fatal("accepted altered original evidence")
	}
	if _, err := store.Retain(ctx, original); err == nil {
		t.Fatal("silently replaced corrupted evidence")
	}
}

func TestCredentialsProtectRotateAndRedactFailures(t *testing.T) {
	ctx := context.Background()
	store, err := NewCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := "linear-account-ref"
	secret := "test-sensitive-value-12345"
	if err := store.Put(ctx, ref, secret); err != nil {
		t.Fatal(err)
	}
	name, _ := credentialName(ref)
	stored, err := os.ReadFile(filepath.Join(store.root, name))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && bytes.Contains(stored, []byte(secret)) {
		t.Fatal("Windows credential was stored as plaintext")
	}
	resolved, err := store.Resolve(ctx, ref)
	if err != nil || resolved != secret {
		t.Fatalf("credential roundtrip failed: %v", err)
	}
	if err := store.Put(ctx, ref, "rotated-test-secret"); err != nil {
		t.Fatal(err)
	}
	resolved, err = store.Resolve(ctx, ref)
	if err != nil || resolved != "rotated-test-secret" {
		t.Fatalf("credential rotation failed: %v", err)
	}
	_, err = store.Resolve(ctx, secret)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), store.root) {
		t.Fatal("missing credential failure did not redact its reference/path")
	}
	if err := store.Put(ctx, ref, "value\r\ninjected-header"); err == nil {
		t.Fatal("credential accepted header injection")
	}
}

func TestResolveRejectsMalformedUnlockedCredentials(t *testing.T) {
	ctx := context.Background()
	store, err := NewCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := "malformed-stored-credential"
	if err := store.Put(ctx, ref, "initial-valid-secret"); err != nil {
		t.Fatal(err)
	}
	name, _ := credentialName(ref)
	path := filepath.Join(store.root, name)
	for _, secret := range []string{"\nBearer injected-header", "valid\r\ninjected-header", "value\x00suffix", " spaced "} {
		protected, err := protectSecret([]byte(secret))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, protected, 0o600); err != nil {
			t.Fatal(err)
		}
		value, err := store.Resolve(ctx, ref)
		if err == nil || value != "" || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
			t.Fatal("malformed stored credential was accepted or exposed in an error")
		}
	}
}

func TestEvidenceIsProtectedBeforePublicationAndConcurrentRetentionIsComplete(t *testing.T) {
	ctx := context.Background()
	store, err := NewEvidenceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("private issue content"), 1024)
	const writers = 12
	var tasks sync.WaitGroup
	results := make(chan error, writers)
	for range writers {
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			ref, err := store.Retain(ctx, content)
			if err == nil {
				var observed []byte
				observed, err = store.Read(ctx, ref)
				if err == nil && !bytes.Equal(observed, content) {
					err = os.ErrInvalid
				}
			}
			results <- err
		}()
	}
	tasks.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(store.root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("concurrent publication left incomplete or duplicate evidence: %v", err)
	}
	file, err := os.Open(filepath.Join(store.root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = file.Close()
	}()
	if err := verifyStoredFile(file); err != nil {
		t.Fatal("source evidence is not restricted to its owner")
	}
}

func TestStoredReadsRejectUnprotectedFilesAndLinks(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "public-file")
	if err := os.WriteFile(path, []byte("private bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := boundedRead(path, 1024); err == nil {
		t.Fatal("read accepted unprotected stored data")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectStoredFile(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	link := filepath.Join(directory, "symlink")
	if err := os.Symlink(path, link); err != nil {
		t.Skip("local account cannot create symbolic links")
	}
	if _, err := boundedRead(link, 1024); err == nil {
		t.Fatal("read followed a stored-data symbolic link")
	}
}
