package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/core/runexport"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

var runIDPattern = regexp.MustCompile(`^run_[0-9A-HJKMNP-TV-Z]{26}$`)

// RunExporter creates one redacted, self-contained ZIP response.
type RunExporter interface {
	Build(context.Context, string) ([]byte, runexport.Manifest, error)
}

func (s *Server) serveRunExport(response http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{
			SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID,
		})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{
			SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run export request does not accept query parameters.", RequestID: requestID,
		})
		return
	}
	clean := path.Clean(request.URL.Path)
	prefix, suffix := "/api/v1/runs/", "/export"
	runID := strings.TrimSuffix(strings.TrimPrefix(clean, prefix), suffix)
	if !strings.HasPrefix(clean, prefix) || !strings.HasSuffix(clean, suffix) || !runIDPattern.MatchString(runID) {
		writeAPIError(response, http.StatusNotFound, apiError{
			SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run export was not found.", RequestID: requestID,
		})
		return
	}
	s.mu.RLock()
	exporter := s.exporter
	s.mu.RUnlock()
	if exporter == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{
			SchemaVersion: 1, Code: "RUN_EXPORT_UNAVAILABLE", Message: "Run export is not configured.", RequestID: requestID, Retryable: true,
		})
		return
	}
	content, manifest, err := exporter.Build(request.Context(), runID)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeAPIError(response, http.StatusNotFound, apiError{
				SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run was not found.", RequestID: requestID,
			})
			return
		}
		writeAPIError(response, http.StatusInternalServerError, apiError{
			SchemaVersion: 1, Code: "RUN_EXPORT_FAILED", Message: "The run export could not be created.", RequestID: requestID, Retryable: true,
		})
		return
	}
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, runID))
	response.Header().Set("X-Darkstar-Export-Schema", fmt.Sprint(manifest.SchemaVersion))
	response.Header().Set("Content-Length", fmt.Sprint(len(content)))
	response.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = response.Write(content)
	}
}
