// Package repositorysnapshot defines immutable, committed repository evidence.
package repositorysnapshot

import "context"

type ResolveRequest struct {
	RepositoryID string
	Root         string
	CommonGitDir string
	Ref          string
}

type ResolvedRevision struct {
	CommitSHA string `json:"commitSha"`
	TreeSHA   string `json:"treeSha"`
}

type ExportRequest struct {
	ScopeID      string   `json:"scopeId"`
	RepositoryID string   `json:"repositoryId"`
	Root         string   `json:"root"`
	CommonGitDir string   `json:"commonGitDir"`
	CommitSHA    string   `json:"commitSha"`
	PathScope    []string `json:"pathScope"`
}

type Exclusion struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type Evidence struct {
	CacheKey       string      `json:"cacheKey"`
	Root           string      `json:"root"`
	ManifestDigest string      `json:"manifestDigest"`
	FileCount      int         `json:"fileCount"`
	TotalBytes     int64       `json:"totalBytes"`
	Exclusions     []Exclusion `json:"exclusions"`
}

// Exporter resolves refs only while freezing a new scope. Materialize receives
// exact committed identity and cannot substitute the current branch or checkout.
type Exporter interface {
	Resolve(context.Context, ResolveRequest) (ResolvedRevision, error)
	Materialize(context.Context, ExportRequest) (Evidence, error)
	Verify(context.Context, Evidence) error
}

type File struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	BlobSHA string `json:"blobSha"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// Manifest is retained outside exposed content roots. It binds every regular
// file to exact Git object bytes; exclusions never grant live source access.
type Manifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	DirtyPolicy   string        `json:"dirtyPolicy"`
	Request       ExportRequest `json:"request"`
	TreeSHA       string        `json:"treeSha"`
	Files         []File        `json:"files"`
	Exclusions    []Exclusion   `json:"exclusions"`
}

type Reader interface {
	Manifest(context.Context, Evidence) (Manifest, error)
	ReadFile(context.Context, Evidence, string) ([]byte, error)
}
