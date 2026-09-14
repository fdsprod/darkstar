package git

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"darkstar/src/platform/process"
	"darkstar/src/ports/repositorysnapshot"
)

type fixture struct {
	base    string
	root    string
	common  string
	commit  string
	adapter *Adapter
}

func newFixture(t *testing.T, limits Limits) fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repository")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, nil, "init", "-b", "main")
	gitRun(t, root, nil, "config", "user.name", "Snapshot test")
	gitRun(t, root, nil, "config", "user.email", "snapshot@example.invalid")
	for name, content := range map[string]string{"tracked.txt": "committed source\n", "src/main.txt": "first\nsecond\n", ".gitattributes": "tracked.txt export-ignore\n"} {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(name)), []byte(content))
	}
	gitRun(t, root, nil, "add", ".")
	gitRun(t, root, nil, "commit", "-m", "original")
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	common := strings.TrimSpace(string(gitRun(t, root, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")))
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(filepath.Join(base, "evidence"), limits)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{base: base, root: canonical, common: common, commit: strings.TrimSpace(string(gitRun(t, root, nil, "rev-parse", "HEAD"))), adapter: adapter}
}

func (f fixture) request() repositorysnapshot.ExportRequest {
	return repositorysnapshot.ExportRequest{ScopeID: "scope_one", RepositoryID: "repo_one", Root: f.root, CommonGitDir: f.common, CommitSHA: f.commit}
}

func gitRun(t *testing.T, root string, input []byte, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Stdin = bytes.NewReader(input)
	process.HideConsole(command)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return output
}

func writeTestFile(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCommittedSnapshotPreservesDirtyCheckoutAndGitState(t *testing.T) {
	f := newFixture(t, Limits{})
	writeTestFile(t, filepath.Join(f.root, "tracked.txt"), []byte("staged change\n"))
	gitRun(t, f.root, nil, "add", "tracked.txt")
	writeTestFile(t, filepath.Join(f.root, "tracked.txt"), []byte("unstaged change\n"))
	writeTestFile(t, filepath.Join(f.root, "private-untracked.txt"), []byte("untracked secret\n"))
	status := gitRun(t, f.root, nil, "status", "--porcelain=v1", "--untracked-files=all")
	index := readTestFile(t, filepath.Join(f.common, "index"))
	head := readTestFile(t, filepath.Join(f.common, "HEAD"))
	trees := gitRun(t, f.root, nil, "worktree", "list", "--porcelain")
	revision, err := f.adapter.Resolve(t.Context(), repositorysnapshot.ResolveRequest{RepositoryID: "repo_one", Root: f.root, CommonGitDir: f.common, Ref: "main"})
	if err != nil || revision.CommitSHA != f.commit || !objectID(revision.TreeSHA) {
		t.Fatalf("resolve: %#v, %v", revision, err)
	}
	evidence, err := f.adapter.Materialize(t.Context(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	value, err := f.adapter.ReadFile(t.Context(), evidence, "tracked.txt")
	if err != nil || string(value) != "committed source\n" {
		t.Fatalf("committed content (ignores export-ignore attribute): %q, %v", value, err)
	}
	if _, err := os.Stat(filepath.Join(evidence.Root, "private-untracked.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("untracked user files entered snapshot")
	}
	if _, err := os.Stat(filepath.Join(evidence.Root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Git metadata entered snapshot")
	}
	if !bytes.Equal(index, readTestFile(t, filepath.Join(f.common, "index"))) || !bytes.Equal(head, readTestFile(t, filepath.Join(f.common, "HEAD"))) {
		t.Fatal("snapshot modified user Git index or HEAD")
	}
	if !bytes.Equal(status, gitRun(t, f.root, nil, "status", "--porcelain=v1", "--untracked-files=all")) || !bytes.Equal(trees, gitRun(t, f.root, nil, "worktree", "list", "--porcelain")) {
		t.Fatal("snapshot modified checkout state or created a worktree")
	}
	if string(readTestFile(t, filepath.Join(f.root, "tracked.txt"))) != "unstaged change\n" || string(readTestFile(t, filepath.Join(f.root, "private-untracked.txt"))) != "untracked secret\n" {
		t.Fatal("snapshot modified user file bytes")
	}
	manifest, err := f.adapter.Manifest(t.Context(), evidence)
	if err != nil || manifest.DirtyPolicy != "committed_only" || manifest.TreeSHA != revision.TreeSHA || len(manifest.Files) != 3 {
		t.Fatalf("manifest: %#v, %v", manifest, err)
	}
	for _, file := range manifest.Files {
		if !objectID(file.BlobSHA) || !objectDigest(file.SHA256) || file.Mode != "100644" {
			t.Fatalf("missing immutable file identity: %#v", file)
		}
	}
}

func TestSnapshotPinsMovedBranchAndReusesWithoutSource(t *testing.T) {
	f := newFixture(t, Limits{})
	selected, err := f.adapter.Resolve(t.Context(), repositorysnapshot.ResolveRequest{RepositoryID: "repo_one", Root: f.root, CommonGitDir: f.common, Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(f.root, "tracked.txt"), []byte("new branch content\n"))
	gitRun(t, f.root, nil, "add", "tracked.txt")
	gitRun(t, f.root, nil, "commit", "-m", "move branch")
	request := f.request()
	request.CommitSHA = selected.CommitSHA
	evidence, err := f.adapter.Materialize(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(readTestFile(t, filepath.Join(evidence.Root, "tracked.txt"))) != "committed source\n" {
		t.Fatal("materialization followed a moving branch")
	}
	if err := os.Rename(f.root, filepath.Join(f.base, "moved-user-checkout")); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(f.adapter.root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.Materialize(t.Context(), request)
	if err != nil || again.ManifestDigest != evidence.ManifestDigest || again.Root != evidence.Root {
		t.Fatalf("retained reuse: %#v, %v", again, err)
	}
	if err := restarted.Verify(t.Context(), evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ReadFile(t.Context(), evidence, "src/main.txt"); err != nil {
		t.Fatal(err)
	}
	request.ScopeID = "different_scope"
	if _, err := restarted.Materialize(t.Context(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing source silently substituted: %v", err)
	}
}

func TestSnapshotScopeEmptyIsDistinctFromInheritedAndChecksMissingRevisions(t *testing.T) {
	f := newFixture(t, Limits{})
	all, err := f.adapter.Materialize(t.Context(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	request := f.request()
	request.PathScope = []string{}
	empty, err := f.adapter.Materialize(t.Context(), request)
	if err != nil || empty.FileCount != 0 || empty.CacheKey == all.CacheKey {
		t.Fatalf("empty override: %#v, %v", empty, err)
	}
	request.PathScope = []string{"src"}
	scoped, err := f.adapter.Materialize(t.Context(), request)
	if err != nil || scoped.FileCount != 1 {
		t.Fatalf("selected scope: %#v, %v", scoped, err)
	}
	if _, err := f.adapter.ReadFile(t.Context(), scoped, "tracked.txt"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unselected file read: %v", err)
	}
	request.PathScope = []string{"./src/"}
	normalized, err := f.adapter.Materialize(t.Context(), request)
	if err != nil || normalized.CacheKey != scoped.CacheKey {
		t.Fatalf("relative path scope normalization: %#v, %v", normalized, err)
	}
	request.PathScope = []string{"missing-directory"}
	if _, err := f.adapter.Materialize(t.Context(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing selected path: %v", err)
	}
	if _, err := f.adapter.Resolve(t.Context(), repositorysnapshot.ResolveRequest{RepositoryID: "repo_one", Root: f.root, CommonGitDir: f.common, Ref: "refs/heads/absent"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing ref: %v", err)
	}
	request.CommitSHA = strings.Repeat("1", 40)
	if _, err := f.adapter.Materialize(t.Context(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing exact commit: %v", err)
	}
	request.CommitSHA = "HEAD"
	if _, err := f.adapter.Materialize(t.Context(), request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutable revision accepted: %v", err)
	}
}

func TestSnapshotDetectsFileManifestExtraAndRootCorruption(t *testing.T) {
	f := newFixture(t, Limits{})
	evidence, err := f.adapter.Materialize(t.Context(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(evidence.Root, "tracked.txt")
	original := readTestFile(t, file)
	writeTestFile(t, file, []byte("corrupt bytes"))
	if err := f.adapter.Verify(t.Context(), evidence); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("file corruption: %v", err)
	}
	if _, err := f.adapter.Materialize(t.Context(), f.request()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt cache silently reused: %v", err)
	}
	writeTestFile(t, file, original)
	extra := filepath.Join(evidence.Root, "injected.txt")
	writeTestFile(t, extra, []byte("extra"))
	if err := f.adapter.Verify(t.Context(), evidence); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unrecorded file: %v", err)
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(evidence.Root, "extra-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.adapter.Verify(t.Context(), evidence); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unrecorded directory: %v", err)
	}
	if err := os.Remove(filepath.Join(evidence.Root, "extra-directory")); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(filepath.Dir(evidence.Root), "manifest.json")
	raw := readTestFile(t, manifestPath)
	var manifest repositorysnapshot.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Exclusions = append(manifest.Exclusions, repositorysnapshot.Exclusion{Path: "ghost", Kind: "symlink", Reason: "forged"})
	modified, _ := json.Marshal(manifest)
	writeTestFile(t, manifestPath, modified)
	if err := f.adapter.Verify(t.Context(), evidence); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("manifest tamper: %v", err)
	}
	writeTestFile(t, manifestPath, raw)
	outside := evidence
	outside.Root = f.root
	if err := f.adapter.Verify(t.Context(), outside); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("outside root accepted: %v", err)
	}
	for _, path := range []string{"../manifest.json", "/absolute", "src\\main.txt", ".git/config"} {
		if _, err := f.adapter.ReadFile(t.Context(), evidence, path); err == nil {
			t.Fatalf("escaped evidence path accepted: %s", path)
		}
	}
}

func TestSnapshotReportsSymlinksGitlinksAndLFSPointersWithoutFollowing(t *testing.T) {
	f := newFixture(t, Limits{})
	link := strings.TrimSpace(string(gitRun(t, f.root, []byte("../../outside-secret"), "hash-object", "-w", "--stdin")))
	lfs := strings.TrimSpace(string(gitRun(t, f.root, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 999\n"), "hash-object", "-w", "--stdin")))
	treeInput := fmt.Sprintf("120000 blob %s\tlink\n160000 commit %s\tsubmodule\n100644 blob %s\tlarge.bin\n", link, f.commit, lfs)
	tree := strings.TrimSpace(string(gitRun(t, f.root, []byte(treeInput), "mktree")))
	commit := strings.TrimSpace(string(gitRun(t, f.root, []byte("special entries\n"), "commit-tree", tree, "-p", f.commit)))
	request := f.request()
	request.CommitSHA = commit
	evidence, err := f.adapter.Materialize(t.Context(), request)
	if err != nil || evidence.FileCount != 0 || len(evidence.Exclusions) != 3 {
		t.Fatalf("unsupported entries: %#v, %v", evidence, err)
	}
	if entries, err := os.ReadDir(evidence.Root); err != nil || len(entries) != 0 {
		t.Fatalf("unsupported objects materialized: %v, %v", entries, err)
	}
}

func TestSnapshotRejectsCaseCollisionsAndResourceLimits(t *testing.T) {
	f := newFixture(t, Limits{})
	blob := strings.TrimSpace(string(gitRun(t, f.root, []byte("same\n"), "hash-object", "-w", "--stdin")))
	tree := strings.TrimSpace(string(gitRun(t, f.root, []byte(fmt.Sprintf("100644 blob %s\tReadme\n100644 blob %s\tREADME\n", blob, blob)), "mktree")))
	commit := strings.TrimSpace(string(gitRun(t, f.root, []byte("case collisions\n"), "commit-tree", tree, "-p", f.commit)))
	request := f.request()
	request.CommitSHA = commit
	if _, err := f.adapter.Materialize(t.Context(), request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("case collisions: %v", err)
	}
	for _, limits := range []Limits{{MaxFiles: 1}, {MaxFileBytes: 1}, {MaxTotalBytes: 2}, {MaxTreeBytes: 8}} {
		adapter, err := New(filepath.Join(t.TempDir(), "bounded"), limits)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Materialize(t.Context(), f.request()); !errors.Is(err, ErrLimit) {
			t.Fatalf("limits %#v: %v", limits, err)
		}
	}
	for _, path := range []string{"../escape", "src/../../escape", "/root", "C:/root", "a\\b", ".git/config", "NUL.txt", "trailing."} {
		request := f.request()
		request.PathScope = []string{path}
		if _, err := f.adapter.Materialize(t.Context(), request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe scope %q: %v", path, err)
		}
	}
}

func TestSnapshotAtomicConcurrentPublicationAndCancelledPreparation(t *testing.T) {
	f := newFixture(t, Limits{})
	var wait sync.WaitGroup
	results := make(chan error, 3)
	for range 3 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := f.adapter.Materialize(t.Context(), f.request())
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(f.adapter.root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("partial staging retained: %v, %v", entries, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := f.request()
	request.ScopeID = "cancelled"
	if _, err := f.adapter.Materialize(ctx, request); err == nil {
		t.Fatal("cancelled preparation succeeded")
	}
}

func TestSnapshotIgnoresCommitAndBlobReplacementRefs(t *testing.T) {
	f := newFixture(t, Limits{})
	originalBlob := strings.TrimSpace(string(gitRun(t, f.root, nil, "rev-parse", "HEAD:tracked.txt")))
	replacementBlob := strings.TrimSpace(string(gitRun(t, f.root, []byte("substituted bytes\n"), "hash-object", "-w", "--stdin")))
	tree := strings.TrimSpace(string(gitRun(t, f.root, []byte(fmt.Sprintf("100644 blob %s\ttracked.txt\n", replacementBlob)), "mktree")))
	replacementCommit := strings.TrimSpace(string(gitRun(t, f.root, []byte("replacement commit\n"), "commit-tree", tree)))
	gitRun(t, f.root, nil, "replace", f.commit, replacementCommit)
	gitRun(t, f.root, nil, "replace", originalBlob, replacementBlob)
	revision, err := f.adapter.Resolve(t.Context(), repositorysnapshot.ResolveRequest{RepositoryID: "repo_one", Root: f.root, CommonGitDir: f.common, Ref: "main"})
	if err != nil || revision.CommitSHA != f.commit {
		t.Fatalf("replaced revision: %#v, %v", revision, err)
	}
	evidence, err := f.adapter.Materialize(t.Context(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	value, err := f.adapter.ReadFile(t.Context(), evidence, "tracked.txt")
	if err != nil || string(value) != "committed source\n" || evidence.FileCount != 3 {
		t.Fatalf("replacement refs changed evidence: %q, %#v, %v", value, evidence, err)
	}
}

func TestSnapshotRejectsRetainedSymlinkAndChangedRepositoryIdentity(t *testing.T) {
	f := newFixture(t, Limits{})
	if _, err := f.adapter.Resolve(t.Context(), repositorysnapshot.ResolveRequest{RepositoryID: "repo_one", Root: f.root, CommonGitDir: f.base, Ref: "main"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed repository identity: %v", err)
	}
	evidence, err := f.adapter.Materialize(t.Context(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(evidence.Root, "tracked.txt")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, "tracked.txt"), file); err != nil {
		t.Skipf("host does not permit symbolic-link creation: %v", err)
	}
	if err := f.adapter.Verify(t.Context(), evidence); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("retained symbolic link accepted: %v", err)
	}
}

func TestSnapshotRejectsBlobBytesStoredUnderWrongGitIdentity(t *testing.T) {
	f := newFixture(t, Limits{})
	blob := strings.TrimSpace(string(gitRun(t, f.root, nil, "rev-parse", "HEAD:tracked.txt")))
	objectPath := filepath.Join(f.common, "objects", blob[:2], blob[2:])
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	for _, data := range [][]byte{[]byte("blob 8\x00"), []byte("corrupt\n")} {
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(objectPath, 0600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, objectPath, compressed.Bytes())
	if _, err := f.adapter.Materialize(t.Context(), f.request()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Git object bytes accepted under incorrect blob identity: %v", err)
	}
}
