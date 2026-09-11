package api

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"darkstar/src/core/contentlibrary"
)

func (s *Server) SetContentLibrary(service *contentlibrary.Service) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("content library must be configured before start")
	}
	s.contentLibrary = service
	return nil
}

func (s *Server) serveContentLibrary(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.contentLibrary
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "CONTENT_LIBRARY_UNAVAILABLE", Message: "Content library is not configured.", RequestID: requestID})
		return
	}
	clean := path.Clean(request.URL.Path)
	action := strings.TrimPrefix(clean, "/api/v1/content-library")
	if action == "" {
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			items, err := service.List(request.Context())
			if err != nil {
				writeContentError(response, requestID, err)
				return
			}
			writeJSON(response, http.StatusOK, struct {
				Items []contentlibrary.Item `json:"items"`
			}{items})
		case http.MethodPost:
			var input contentlibrary.CreateRequest
			if err := decodeWorkflowJSON(request, &input); err != nil {
				writeContentError(response, requestID, err)
				return
			}
			item, err := service.Create(request.Context(), input)
			writeContentResult(response, requestID, item, err)
		default:
			writeWorkflowMethod(response, requestID, "GET, HEAD, POST")
		}
		return
	}
	if action == "/preview" {
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input struct {
			Document     contentlibrary.Document `json:"document"`
			LinkedInputs []string                `json:"linkedInputs"`
			Revision     bool                    `json:"revision"`
		}
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeContentError(response, requestID, err)
			return
		}
		preview, err := contentlibrary.BuildPrompt(input.Document, input.LinkedInputs, input.Revision)
		if err != nil {
			writeContentError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, preview)
		return
	}
	parts := strings.Split(strings.TrimPrefix(action, "/"), "/")
	if len(parts) == 1 && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
		item, err := service.Get(request.Context(), parts[0])
		writeContentResult(response, requestID, item, err)
		return
	}
	if len(parts) != 2 {
		writeContentError(response, requestID, contentlibrary.ErrNotFound)
		return
	}
	method := http.MethodPost
	if parts[1] == "draft" {
		method = http.MethodPut
	}
	if request.Method != method {
		writeWorkflowMethod(response, requestID, method)
		return
	}
	var item contentlibrary.Item
	var err error
	switch parts[1] {
	case "draft":
		var input contentlibrary.UpdateRequest
		err = decodeWorkflowJSON(request, &input)
		if err == nil {
			item, err = service.Update(request.Context(), parts[0], input)
		}
	case "publish":
		var input contentlibrary.PublishRequest
		err = decodeWorkflowJSON(request, &input)
		if err == nil {
			item, err = service.Publish(request.Context(), parts[0], input)
		}
	case "duplicate":
		var input struct {
			Name string `json:"name"`
		}
		err = decodeWorkflowJSON(request, &input)
		if err == nil {
			item, err = service.Duplicate(request.Context(), parts[0], input.Name)
		}
	case "archive", "restore":
		var input struct{}
		err = decodeWorkflowJSON(request, &input)
		if err == nil {
			item, err = service.Archive(request.Context(), parts[0], parts[1] == "archive")
		}
	default:
		err = contentlibrary.ErrNotFound
	}
	writeContentResult(response, requestID, item, err)
}

func writeContentResult(response http.ResponseWriter, requestID string, item contentlibrary.Item, err error) {
	if err != nil {
		writeContentError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, item)
}

func writeContentError(response http.ResponseWriter, requestID string, err error) {
	status := http.StatusBadRequest
	code := "VALIDATION_FAILED"
	if errors.Is(err, contentlibrary.ErrNotFound) {
		status = http.StatusNotFound
		code = "NOT_FOUND"
	} else if errors.Is(err, contentlibrary.ErrConflict) {
		status = http.StatusConflict
		code = "CONFLICT"
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: err.Error(), RequestID: requestID})
}
