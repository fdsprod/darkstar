package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactdiff"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/lateevidence"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/impactassessment"
	"darkstar/src/ports/representationregistry"
)

const maxArtifactRequestBytes = 36 << 20

type ArtifactService interface {
	Ingest(context.Context, artifactops.IngestInput, string) (artifactingest.Result, error)
	Revise(context.Context, string, uint64, artifactops.IngestInput, string) (artifactingest.Result, error)
	Attach(context.Context, artifactops.AttachInput, string) (artifactbinding.Version, error)
	Detach(context.Context, string, string) (artifactbinding.Version, error)
	List(context.Context, artifactops.ListInput) ([]artifactops.ArtifactView, error)
	Show(context.Context, string, uint64) (artifactops.ArtifactView, error)
	Representations(context.Context, artifactregistry.VersionRef) ([]representationregistry.Representation, error)
	Extract(context.Context, artifactregistry.VersionRef, string) (artifactderive.Result, error)
	Diff(context.Context, artifactops.DiffInput) (artifactops.VersionDiff, error)
	Lint(context.Context, artifactregistry.VersionRef) (artifactops.LintResult, error)
	Impact(context.Context, lateevidence.Request) (impactassessment.Assessment, error)
	OriginalContent(context.Context, artifactregistry.VersionRef) (artifactops.Content, error)
	RepresentationContent(context.Context, string) (artifactops.Content, error)
}

func (s *Server) serveArtifacts(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.artifacts
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "ARTIFACT_SERVICE_UNAVAILABLE", Message: "Artifact operations are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := strings.Trim(path.Clean(request.URL.Path), "/")
	segments := strings.Split(clean, "/")
	if len(segments) < 3 {
		writeArtifactError(response, requestID, errors.New("invalid artifact route"))
		return
	}
	if segments[2] == "artifact-bindings" {
		s.serveArtifactBindings(response, request, requestID, service, segments[3:])
		return
	}
	if segments[2] == "representations" {
		s.serveRepresentationContent(response, request, requestID, service, segments[3:])
		return
	}
	if segments[2] != "artifacts" {
		writeArtifactError(response, requestID, errors.New("invalid artifact route"))
		return
	}
	if len(segments) == 3 {
		s.serveArtifactCollection(response, request, requestID, service)
		return
	}
	artifactID := segments[3]
	if !strings.HasPrefix(artifactID, "artifact_") {
		writeArtifactError(response, requestID, artifactregistry.ErrNotFound)
		return
	}
	if len(segments) == 4 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeArtifactMethod(response, requestID, "GET, HEAD")
			return
		}
		version, err := queryVersion(request.URL.Query(), "version", false)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Show(request.Context(), artifactID, version)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
		return
	}
	if len(segments) != 5 {
		writeArtifactError(response, requestID, artifactregistry.ErrNotFound)
		return
	}
	switch segments[4] {
	case "content":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeArtifactMethod(response, requestID, "GET, HEAD")
			return
		}
		if len(request.URL.Query()) != 1 || len(request.URL.Query()["version"]) != 1 {
			writeArtifactError(response, requestID, errors.New("content requests require exactly one version query parameter"))
			return
		}
		version, err := queryVersion(request.URL.Query(), "version", true)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		content, err := service.OriginalContent(request.Context(), artifactregistry.VersionRef{ArtifactID: artifactID, Version: version})
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		serveArtifactContent(response, request, content, false)
	case "revisions":
		if request.Method != http.MethodPost {
			writeArtifactMethod(response, requestID, "POST")
			return
		}
		baseVersion, err := parseIfMatch(request.Header.Get("If-Match"))
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		var input artifactops.IngestInput
		if err := decodeArtifactJSON(request, &input); err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		key, err := artifactIdempotencyKey(request)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Revise(request.Context(), artifactID, baseVersion, input, key)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		response.Header().Set("ETag", `"`+strconv.FormatUint(value.Artifact.Version, 10)+`"`)
		writeJSON(response, http.StatusCreated, value)
	case "diff":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeArtifactMethod(response, requestID, "GET, HEAD")
			return
		}
		from, err := queryVersion(request.URL.Query(), "from", true)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		to, err := queryVersion(request.URL.Query(), "to", true)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		query := request.URL.Query()
		allowed := map[string]bool{"from": true, "to": true, "fromRepresentationId": true, "toRepresentationId": true, "cursor": true, "limit": true}
		for key, values := range query {
			if !allowed[key] || len(values) != 1 {
				writeArtifactError(response, requestID, errors.New("diff query parameters are invalid"))
				return
			}
			if key != "from" && key != "to" && values[0] == "" {
				writeArtifactError(response, requestID, errors.New("diff optional query parameters cannot be empty"))
				return
			}
		}
		limit := 0
		if value := query.Get("limit"); value != "" {
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > artifactdiff.MaxPageSize {
				writeArtifactError(response, requestID, errors.New("limit must be between 1 and 200"))
				return
			}
			limit = parsed
		}
		for _, name := range []string{"fromRepresentationId", "toRepresentationId"} {
			if value := query.Get(name); value != "" && !strings.HasPrefix(value, "representation_") {
				writeArtifactError(response, requestID, errors.New(name+" must be a representation ID"))
				return
			}
		}
		if len(query.Get("cursor")) > 256 {
			writeArtifactError(response, requestID, errors.New("cursor is too long"))
			return
		}
		value, err := service.Diff(request.Context(), artifactops.DiffInput{ArtifactID: artifactID, From: from, To: to, FromRepresentationID: query.Get("fromRepresentationId"), ToRepresentationID: query.Get("toRepresentationId"), Cursor: query.Get("cursor"), Limit: limit})
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "extract":
		if request.Method != http.MethodPost {
			writeArtifactMethod(response, requestID, "POST")
			return
		}
		version, err := queryVersion(request.URL.Query(), "version", true)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		key, err := artifactIdempotencyKey(request)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Extract(request.Context(), artifactregistry.VersionRef{ArtifactID: artifactID, Version: version}, key)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "lint":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeArtifactMethod(response, requestID, "GET, HEAD")
			return
		}
		version, err := queryVersion(request.URL.Query(), "version", true)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Lint(request.Context(), artifactregistry.VersionRef{ArtifactID: artifactID, Version: version})
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "representations":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeArtifactMethod(response, requestID, "GET, HEAD")
			return
		}
		version, err := queryVersion(request.URL.Query(), "version", true)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Representations(request.Context(), artifactregistry.VersionRef{ArtifactID: artifactID, Version: version})
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "impact":
		if request.Method != http.MethodPost {
			writeArtifactMethod(response, requestID, "POST")
			return
		}
		version, err := queryVersion(request.URL.Query(), "version", true)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		var input struct {
			Target artifactbinding.Target `json:"target"`
			RunID  string                 `json:"runId,omitempty"`
		}
		if err := decodeArtifactJSON(request, &input); err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Impact(request.Context(), lateevidence.Request{Evidence: artifactregistry.VersionRef{ArtifactID: artifactID, Version: version}, Target: input.Target, RunID: input.RunID})
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	default:
		writeArtifactError(response, requestID, artifactregistry.ErrNotFound)
	}
}

func (s *Server) serveRepresentationContent(response http.ResponseWriter, request *http.Request, requestID string, service ArtifactService, remainder []string) {
	if len(remainder) != 2 || remainder[1] != "content" {
		writeArtifactError(response, requestID, representationregistry.ErrNotFound)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeArtifactMethod(response, requestID, "GET, HEAD")
		return
	}
	if request.URL.RawQuery != "" || !strings.HasPrefix(remainder[0], "representation_") {
		writeArtifactError(response, requestID, representationregistry.ErrNotFound)
		return
	}
	content, err := service.RepresentationContent(request.Context(), remainder[0])
	if err != nil {
		writeArtifactError(response, requestID, err)
		return
	}
	serveArtifactContent(response, request, content, true)
}

func serveArtifactContent(response http.ResponseWriter, request *http.Request, content artifactops.Content, allowInline bool) {
	defer func() { _ = content.Reader.Close() }()
	mediaType, disposition := artifactContentHeaders(content.MediaType, allowInline)
	response.Header().Set("Content-Type", mediaType)
	response.Header().Set("Content-Disposition", disposition+`; filename="`+safeArtifactFileName(content.FileName)+`"`)
	response.Header().Set("Content-Length", strconv.FormatInt(content.Size, 10))
	response.Header().Set("ETag", `"`+content.Digest+`"`)
	response.Header().Set("X-Darkstar-Content-Digest", "sha256="+content.Digest)
	if request.Method == http.MethodHead {
		response.WriteHeader(http.StatusOK)
		return
	}
	response.WriteHeader(http.StatusOK)
	_, _ = io.Copy(response, content.Reader)
}

func artifactContentHeaders(value string, allowInline bool) (string, string) {
	base, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "application/octet-stream", "attachment"
	}
	base = strings.ToLower(base)
	if allowInline {
		switch base {
		case "text/plain", "text/markdown", "text/csv", "application/json", "image/png", "image/jpeg", "image/webp":
			return base, "inline"
		}
	}
	return "application/octet-stream", "attachment"
}

func safeArtifactFileName(value string) string {
	value = strings.Map(func(character rune) rune {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9':
			return character
		case character == '.', character == '-', character == '_', character == ' ':
			return character
		default:
			return '_'
		}
	}, value)
	value = strings.Trim(value, " .")
	if value == "" || value == "." || value == ".." {
		return "artifact-download"
	}
	if len(value) > 180 {
		value = value[:180]
	}
	return value
}

func (s *Server) serveArtifactCollection(response http.ResponseWriter, request *http.Request, requestID string, service ArtifactService) {
	switch request.Method {
	case http.MethodPost:
		var input artifactops.IngestInput
		if err := decodeArtifactJSON(request, &input); err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		key, err := artifactIdempotencyKey(request)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Ingest(request.Context(), input, key)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		response.Header().Set("Location", "/api/v1/artifacts/"+value.Artifact.ArtifactID+"?version="+strconv.FormatUint(value.Artifact.Version, 10))
		writeJSON(response, http.StatusCreated, value)
	case http.MethodGet, http.MethodHead:
		query := request.URL.Query()
		var input artifactops.ListInput
		if query.Has("targetKind") || query.Has("targetId") {
			if query.Get("targetKind") == "" || query.Get("targetId") == "" {
				writeArtifactError(response, requestID, errors.New("targetKind and targetId must be provided together"))
				return
			}
			target := artifactbinding.Target{Kind: artifactbinding.TargetKind(query.Get("targetKind")), ID: query.Get("targetId")}
			input.Target = &target
		}
		value, err := service.List(request.Context(), input)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	default:
		writeArtifactMethod(response, requestID, "GET, HEAD, POST")
	}
}

func (s *Server) serveArtifactBindings(response http.ResponseWriter, request *http.Request, requestID string, service ArtifactService, remainder []string) {
	if len(remainder) == 0 {
		if request.Method != http.MethodPost {
			writeArtifactMethod(response, requestID, "POST")
			return
		}
		var input artifactops.AttachInput
		if err := decodeArtifactJSON(request, &input); err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		key, err := artifactIdempotencyKey(request)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		value, err := service.Attach(request.Context(), input, key)
		if err != nil {
			writeArtifactError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
		return
	}
	if len(remainder) != 1 || request.Method != http.MethodDelete {
		writeArtifactMethod(response, requestID, "DELETE")
		return
	}
	key, err := artifactIdempotencyKey(request)
	if err != nil {
		writeArtifactError(response, requestID, err)
		return
	}
	value, err := service.Detach(request.Context(), remainder[0], key)
	if err != nil {
		writeArtifactError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func decodeArtifactJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxArtifactRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("request must contain one valid JSON object")
	}
	return nil
}

func artifactIdempotencyKey(request *http.Request) (string, error) {
	key := request.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
		return "", errors.New("Idempotency-Key must contain 8 to 128 non-padding characters")
	}
	return key, nil
}

func queryVersion(query url.Values, name string, required bool) (uint64, error) {
	value := query.Get(name)
	if value == "" && !required {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New(name + " must be a positive artifact version")
	}
	return parsed, nil
}

func writeArtifactMethod(response http.ResponseWriter, requestID, allow string) {
	response.Header().Set("Allow", allow)
	writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
}

func writeArtifactError(response http.ResponseWriter, requestID string, err error) {
	status, code, message := http.StatusBadRequest, "VALIDATION_FAILED", err.Error()
	if errors.Is(err, artifactregistry.ErrNotFound) || errors.Is(err, artifactbinding.ErrNotFound) || errors.Is(err, representationregistry.ErrNotFound) {
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The requested artifact resource was not found."
	} else if errors.Is(err, artifactops.ErrContentWithheld) {
		status, code, message = http.StatusForbidden, "ARTIFACT_CONTENT_WITHHELD", "Artifact content is withheld by inspection or disclosure policy."
	} else if errors.Is(err, artifactops.ErrDiffStorage) {
		status, code, message = http.StatusInternalServerError, "ARTIFACT_DIFF_STORAGE_FAILED", "Artifact representation storage verification failed."
	} else if errors.Is(err, artifactregistry.ErrVersionConflict) || errors.Is(err, artifactbinding.ErrConflict) || errors.Is(err, artifactbinding.ErrStateConflict) || errors.Is(err, lateevidence.ErrEvidenceNotBound) {
		status, code = http.StatusConflict, "ARTIFACT_CONFLICT"
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: message, RequestID: requestID})
}
