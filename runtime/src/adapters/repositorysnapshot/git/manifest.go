package git

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"darkstar/src/ports/repositorysnapshot"
)

var _ repositorysnapshot.Reader = (*Adapter)(nil)

func (a *Adapter) export(ctx context.Context, request repositorysnapshot.ExportRequest, content string) (repositorysnapshot.Manifest, error) {
	manifest := repositorysnapshot.Manifest{SchemaVersion: 1, DirtyPolicy: "committed_only", Request: request, Files: []repositorysnapshot.File{}, Exclusions: []repositorysnapshot.Exclusion{}}
	tree, err := a.git(ctx, request.Root, 256, "rev-parse", "--verify", "--end-of-options", request.CommitSHA+"^{tree}")
	if err != nil || !objectID(strings.TrimSpace(string(tree))) {
		return manifest, fmt.Errorf("%w: recorded commit tree is unavailable", ErrUnavailable)
	}
	manifest.TreeSHA = strings.TrimSpace(string(tree))
	listing, err := a.git(ctx, request.Root, a.limits.MaxTreeBytes, "ls-tree", "--full-tree", "-r", "-z", "-l", request.CommitSHA)
	if err != nil {
		return manifest, err
	}
	paths := newPathInventory()
	matched := make(map[string]bool)
	var pending []repositorysnapshot.File
	var total int64
	selected := 0
	for _, entry := range bytes.Split(listing, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		header, path, found := bytes.Cut(entry, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !found || len(fields) != 4 || !objectID(fields[2]) {
			return manifest, fmt.Errorf("%w: malformed Git tree entry", ErrUnavailable)
		}
		name := string(path)
		if !included(name, request.PathScope) {
			continue
		}
		for _, prefix := range request.PathScope {
			if included(name, []string{prefix}) {
				matched[prefix] = true
			}
		}
		selected++
		if selected > a.limits.MaxFiles {
			return manifest, fmt.Errorf("%w: too many selected tree entries", ErrLimit)
		}
		if err := paths.add(name); err != nil {
			return manifest, err
		}
		switch fields[0] {
		case "120000":
			manifest.Exclusions = append(manifest.Exclusions, repositorysnapshot.Exclusion{Path: name, Kind: "symlink", Reason: "Committed symbolic link excluded; its target is never followed."})
			continue
		case "160000":
			manifest.Exclusions = append(manifest.Exclusions, repositorysnapshot.Exclusion{Path: name, Kind: "gitlink", Reason: "Submodule content is not part of this repository tree and was not fetched."})
			continue
		case "100644", "100755":
		default:
			return manifest, fmt.Errorf("%w: unsupported tree mode at %s", ErrUnavailable, name)
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 || fields[1] != "blob" {
			return manifest, fmt.Errorf("%w: malformed file entry at %s", ErrUnavailable, name)
		}
		if size > a.limits.MaxFileBytes || size > a.limits.MaxTotalBytes-total {
			return manifest, fmt.Errorf("%w: file %s exceeds byte limits", ErrLimit, name)
		}
		pending = append(pending, repositorysnapshot.File{Path: name, Mode: fields[0], BlobSHA: fields[2], Size: size})
		total += size
	}
	for _, prefix := range request.PathScope {
		if prefix != "." && !matched[prefix] {
			return manifest, fmt.Errorf("%w: selected path scope %q is absent from the recorded commit", ErrUnavailable, prefix)
		}
	}
	values, err := a.readBlobs(ctx, request.Root, pending, total)
	if err != nil {
		return manifest, err
	}
	for index, entry := range pending {
		value := values[index]
		if bytes.HasPrefix(value, []byte("version https://git-lfs.github.com/spec/v1\n")) || bytes.HasPrefix(value, []byte("version https://git-lfs.github.com/spec/v1\r\n")) {
			manifest.Exclusions = append(manifest.Exclusions, repositorysnapshot.Exclusion{Path: entry.Path, Kind: "lfs_pointer", Reason: "Committed Git LFS pointer excluded; external content was not fetched."})
			continue
		}
		file := filepath.Join(content, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return manifest, err
		}
		if err := writeExclusive(file, value); err != nil {
			return manifest, err
		}
		entry.SHA256 = digest(value)
		manifest.Files = append(manifest.Files, entry)
	}
	return manifest, nil
}

func (a *Adapter) readBlobs(ctx context.Context, root string, files []repositorysnapshot.File, total int64) ([][]byte, error) {
	values := make([][]byte, 0, len(files))
	if len(files) == 0 {
		return values, nil
	}
	var input strings.Builder
	for _, file := range files {
		input.WriteString(file.BlobSHA + "\n")
	}
	// One bounded object batch avoids a process per source file. Headers and
	// trailing newlines get a separate fixed allowance per admitted file.
	output, err := a.gitInput(ctx, root, total+int64(len(files))*256, []byte(input.String()), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		header, rest, found := bytes.Cut(output, []byte{'\n'})
		fields := strings.Fields(string(header))
		if !found || len(fields) != 3 || fields[0] != file.BlobSHA || fields[1] != "blob" || fields[2] != strconv.FormatInt(file.Size, 10) || int64(len(rest)) <= file.Size {
			return nil, fmt.Errorf("%w: malformed or unavailable committed blob %s", ErrUnavailable, file.Path)
		}
		value := rest[:int(file.Size)]
		if rest[int(file.Size)] != '\n' || gitBlobID(value, len(file.BlobSHA)) != file.BlobSHA {
			return nil, fmt.Errorf("%w: committed blob identity mismatch at %s", ErrUnavailable, file.Path)
		}
		values = append(values, value)
		output = rest[int(file.Size)+1:]
	}
	if len(output) != 0 {
		return nil, fmt.Errorf("%w: unexpected committed object batch content", ErrUnavailable)
	}
	return values, nil
}

func (a *Adapter) Manifest(ctx context.Context, evidence repositorysnapshot.Evidence) (repositorysnapshot.Manifest, error) {
	if err := a.Verify(ctx, evidence); err != nil {
		return repositorysnapshot.Manifest{}, err
	}
	manifest, raw, err := a.loadManifest(evidence.CacheKey)
	if err == nil && digest(raw) != evidence.ManifestDigest {
		return repositorysnapshot.Manifest{}, fmt.Errorf("%w: manifest changed during access", ErrCorrupt)
	}
	return manifest, err
}

func (a *Adapter) ReadFile(ctx context.Context, evidence repositorysnapshot.Evidence, path string) ([]byte, error) {
	if err := portablePath(path); err != nil {
		return nil, err
	}
	manifest, err := a.Manifest(ctx, evidence)
	if err != nil {
		return nil, err
	}
	for _, entry := range manifest.Files {
		if entry.Path != path {
			continue
		}
		file := filepath.Join(evidence.Root, filepath.FromSlash(path))
		if err := a.safeDescendant(file); err != nil {
			return nil, err
		}
		value, err := readBounded(file, a.limits.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		if int64(len(value)) != entry.Size || digest(value) != entry.SHA256 || gitBlobID(value, len(entry.BlobSHA)) != entry.BlobSHA {
			return nil, fmt.Errorf("%w: requested file changed", ErrCorrupt)
		}
		return value, nil
	}
	return nil, fmt.Errorf("%w: path is not a materialized file in the frozen manifest", ErrInvalid)
}

func (a *Adapter) readEvidence(ctx context.Context, key string) (repositorysnapshot.Evidence, error) {
	manifest, raw, err := a.loadManifest(key)
	if err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	content := filepath.Join(a.root, key, "content")
	if err := a.verifyFiles(ctx, content, manifest); err != nil {
		return repositorysnapshot.Evidence{}, err
	}
	evidence := repositorysnapshot.Evidence{CacheKey: key, Root: content, ManifestDigest: digest(raw), FileCount: len(manifest.Files), Exclusions: manifest.Exclusions}
	for _, entry := range manifest.Files {
		evidence.TotalBytes += entry.Size
	}
	return evidence, nil
}

func (a *Adapter) loadManifest(key string) (repositorysnapshot.Manifest, []byte, error) {
	var manifest repositorysnapshot.Manifest
	if !objectDigest(key) {
		return manifest, nil, fmt.Errorf("%w: invalid snapshot cache identity", ErrCorrupt)
	}
	path := filepath.Join(a.root, key, "manifest.json")
	if err := a.safeDescendant(path); err != nil {
		return manifest, nil, err
	}
	raw, err := readBounded(path, a.limits.MaxTreeBytes*4)
	if err != nil {
		return manifest, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, nil, fmt.Errorf("%w: invalid snapshot manifest", ErrCorrupt)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return manifest, nil, fmt.Errorf("%w: trailing manifest content", ErrCorrupt)
	}
	_, expectedKey, err := normalizeRequest(manifest.Request)
	if err != nil || expectedKey != key || manifest.SchemaVersion != 1 || manifest.DirtyPolicy != "committed_only" || !objectID(manifest.TreeSHA) {
		return manifest, nil, fmt.Errorf("%w: manifest identity differs from frozen export request", ErrCorrupt)
	}
	return manifest, raw, nil
}

func (a *Adapter) verifyFiles(ctx context.Context, content string, manifest repositorysnapshot.Manifest) error {
	if len(manifest.Files)+len(manifest.Exclusions) > a.limits.MaxFiles {
		return ErrLimit
	}
	if err := a.safeDescendant(content); err != nil {
		return err
	}
	info, err := os.Lstat(content)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: snapshot content root is not a directory", ErrCorrupt)
	}
	paths := newPathInventory()
	expected := make(map[string]repositorysnapshot.File, len(manifest.Files))
	directories := map[string]bool{".": true}
	var total int64
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := paths.add(entry.Path); err != nil {
			return fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		if !included(entry.Path, manifest.Request.PathScope) || (entry.Mode != "100644" && entry.Mode != "100755") || !objectID(entry.BlobSHA) || !objectDigest(entry.SHA256) || entry.Size < 0 || entry.Size > a.limits.MaxFileBytes || entry.Size > a.limits.MaxTotalBytes-total {
			return fmt.Errorf("%w: invalid manifest file entry", ErrCorrupt)
		}
		expected[entry.Path] = entry
		total += entry.Size
		for directory := filepath.Dir(filepath.FromSlash(entry.Path)); directory != "."; directory = filepath.Dir(directory) {
			directories[filepath.ToSlash(directory)] = true
		}
	}
	for _, exclusion := range manifest.Exclusions {
		if err := paths.add(exclusion.Path); err != nil {
			return fmt.Errorf("%w: invalid exclusion path: %v", ErrCorrupt, err)
		}
		if !included(exclusion.Path, manifest.Request.PathScope) || (exclusion.Kind != "symlink" && exclusion.Kind != "gitlink" && exclusion.Kind != "lfs_pointer") || exclusion.Reason == "" {
			return fmt.Errorf("%w: invalid evidence exclusion", ErrCorrupt)
		}
	}
	seen := 0
	err = filepath.WalkDir(content, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic link in retained evidence", ErrCorrupt)
		}
		relative, err := filepath.Rel(content, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if entry.IsDir() {
			if !directories[name] {
				return fmt.Errorf("%w: unrecorded directory in retained evidence", ErrCorrupt)
			}
			return nil
		}
		file, exists := expected[name]
		if !exists {
			return fmt.Errorf("%w: unrecorded file in retained evidence", ErrCorrupt)
		}
		value, err := readBounded(path, a.limits.MaxFileBytes)
		if err != nil {
			return err
		}
		if int64(len(value)) != file.Size || digest(value) != file.SHA256 || gitBlobID(value, len(file.BlobSHA)) != file.BlobSHA {
			return fmt.Errorf("%w: committed evidence file changed at %s", ErrCorrupt, name)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(expected) {
		return fmt.Errorf("%w: recorded evidence files are missing", ErrCorrupt)
	}
	return nil
}

func (a *Adapter) checkStorage() error {
	canonical, err := filepath.EvalSymlinks(a.root)
	if err != nil || !samePath(canonical, a.root) {
		return fmt.Errorf("%w: snapshot storage moved or contains a symbolic link", ErrCorrupt)
	}
	return nil
}

func (a *Adapter) safeDescendant(path string) error {
	if err := a.checkStorage(); err != nil {
		return err
	}
	relative, err := filepath.Rel(a.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("%w: evidence path escapes storage", ErrCorrupt)
	}
	current := a.root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: evidence path is missing or follows a symbolic link", ErrCorrupt)
		}
	}
	return nil
}

func included(path string, scope []string) bool {
	if scope == nil {
		return true
	}
	for _, prefix := range scope {
		if prefix == "." || path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func portablePath(path string) error {
	if path == "" || len(path) > 4096 || !utf8.ValidString(path) || strings.ContainsAny(path, "\\\x00\r\n\t<>:\"|?*") || strings.HasPrefix(path, "/") {
		return fmt.Errorf("%w: unsafe committed path %q", ErrInvalid, path)
	}
	if strings.IndexFunc(path, func(char rune) bool {
		return char < 32 || char == 127
	}) >= 0 {
		return fmt.Errorf("%w: control character in committed path", ErrInvalid)
	}
	segments := strings.Split(path, "/")
	if len(segments) > 64 {
		return fmt.Errorf("%w: committed path is too deeply nested", ErrLimit)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.TrimRight(segment, " .") != segment {
			return fmt.Errorf("%w: unsafe committed path %q", ErrInvalid, path)
		}
		stem := strings.ToUpper(strings.SplitN(segment, ".", 2)[0])
		if strings.EqualFold(segment, ".git") || stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return fmt.Errorf("%w: committed path %q is not portable", ErrInvalid, path)
		}
	}
	return nil
}

func gitBlobID(value []byte, width int) string {
	// Git object IDs hash the object header and bytes. SHA-1 here is the Git
	// repository's identity format; evidence integrity also records SHA-256.
	header := []byte(fmt.Sprintf("blob %d\x00", len(value)))
	if width == 40 {
		hash := sha1.New()
		_, _ = hash.Write(header)
		_, _ = hash.Write(value)
		return fmt.Sprintf("%x", hash.Sum(nil))
	}
	hash := sha256.New()
	_, _ = hash.Write(header)
	_, _ = hash.Write(value)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

type pathInventory struct {
	spellings map[string]string
	files     map[string]bool
}

func newPathInventory() *pathInventory {
	return &pathInventory{spellings: map[string]string{}, files: map[string]bool{}}
}

func (p *pathInventory) add(path string) error {
	if err := portablePath(path); err != nil {
		return err
	}
	parts := strings.Split(path, "/")
	for index := range parts {
		prefix := strings.Join(parts[:index+1], "/")
		key := strings.ToLower(prefix)
		spelling, exists := p.spellings[key]
		if exists && (spelling != prefix || p.files[key] || index == len(parts)-1) {
			return fmt.Errorf("%w: case or file/directory collision at %s", ErrInvalid, path)
		}
		p.spellings[key] = prefix
	}
	p.files[strings.ToLower(path)] = true
	return nil
}
