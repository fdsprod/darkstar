package api

import (
	"context"
	"net/http"
	"path"
	"strings"

	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

// ProjectRepositoryService exposes version-two project representations through
// the existing authenticated v1 transport, shared by CLI and dashboard clients.
type ProjectRepositoryService interface {
	CreateProjectV2(context.Context, workmanagement.CreateProjectRequest, string) (workmanagement.ProjectRepositoriesView, error)
	ProjectsV2(context.Context) ([]workmanagement.ProjectRepositoriesView, error)
	ProjectRepositories(context.Context, string) (workmanagement.ProjectRepositoriesView, error)
	SetMembership(context.Context, workmanagement.MembershipRequest, string) (workmanagement.ProjectRepositoriesView, error)
	RemoveMembership(context.Context, workmanagement.RemoveMembershipRequest, string) (workmanagement.ProjectRepositoriesView, error)
	UpdateProjectDefaults(context.Context, workmanagement.ProjectDefaultsRequest, string) (workmanagement.ProjectRepositoriesView, error)
	DiscoverProjects(context.Context, string) ([]workmanagement.ProjectRepositoriesView, error)
}

// RepositoryMembershipInput addresses either a local path when attaching a
// repository or the repository ID in the resource path when editing it.
type RepositoryMembershipInput struct {
	RepositoryPath             string                        `json:"repositoryPath,omitempty"`
	Label                      string                        `json:"label"`
	Role                       statestore.RepositoryRole     `json:"role"`
	Settings                   statestore.RepositorySettings `json:"settings"`
	ExpectedMembershipRevision uint64                        `json:"expectedMembershipRevision"`
}

type RepositoryMembershipRemovalInput struct {
	ExpectedMembershipRevision uint64 `json:"expectedMembershipRevision"`
}

func (s *Server) serveProjectRepositories(response http.ResponseWriter, request *http.Request, requestID string) {
	service, ok := s.workService().(ProjectRepositoryService)
	if !ok {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORK_SERVICE_UNAVAILABLE", Message: "Repository membership operations are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	resource := strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/projects-v2")
	if resource == "/discover" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
		query := request.URL.Query()
		if len(query) != 1 || len(query["path"]) != 1 {
			writeWorkError(response, requestID, workmanagement.ErrInvalidRequest)
			return
		}
		location, err := existingAbsoluteDirectory(query.Get("path"))
		if err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		value, err := service.DiscoverProjects(request.Context(), location)
		if err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
		return
	}
	if request.URL.RawQuery != "" {
		writeWorkError(response, requestID, workmanagement.ErrInvalidRequest)
		return
	}
	if resource == "" {
		if request.Method == http.MethodGet || request.Method == http.MethodHead {
			value, err := service.ProjectsV2(request.Context())
			if err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			writeJSON(response, http.StatusOK, value)
			return
		}
		if request.Method != http.MethodPost {
			writeWorkMethod(response, requestID, "GET, HEAD, POST")
			return
		}
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		var input workmanagement.CreateProjectRequest
		if err := decodeWorkJSON(request, &input); err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		value, err := service.CreateProjectV2(request.Context(), input, key)
		if err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		response.Header().Set("Location", "/api/v1/projects-v2/"+value.Project.ProjectID)
		writeJSON(response, http.StatusCreated, value)
		return
	}
	parts := strings.Split(strings.TrimPrefix(resource, "/"), "/")
	if !projectIDPattern.MatchString(parts[0]) || len(parts) > 3 {
		writeWorkNotFound(response, requestID, "project repository resource")
		return
	}
	projectID := parts[0]
	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "repositories" && (request.Method == http.MethodGet || request.Method == http.MethodHead)) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
		value, err := service.ProjectRepositories(request.Context(), projectID)
		if err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
		return
	}
	if parts[1] != "repositories" && (parts[1] != "defaults" || len(parts) != 2) {
		writeWorkNotFound(response, requestID, "project repository resource")
		return
	}
	allowed := "PUT"
	if parts[1] == "repositories" {
		allowed = "GET, HEAD, POST"
		if len(parts) == 3 {
			allowed = "PUT, DELETE"
		}
	}
	if !strings.Contains(", "+allowed+", ", ", "+request.Method+", ") {
		writeWorkMethod(response, requestID, allowed)
		return
	}
	key, ok := requireIdempotencyKey(response, request, requestID)
	if !ok {
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeWorkError(response, requestID, fmtWorkInvalid(err.Error()))
		return
	}
	var value workmanagement.ProjectRepositoriesView
	if parts[1] == "defaults" {
		var input struct {
			Defaults statestore.RepositorySettings `json:"defaults"`
		}
		if err := decodeWorkJSON(request, &input); err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		value, err = service.UpdateProjectDefaults(request.Context(), workmanagement.ProjectDefaultsRequest{ProjectID: projectID, Defaults: input.Defaults, ExpectedVersion: expected}, key)
	} else if request.Method == http.MethodDelete {
		var input RepositoryMembershipRemovalInput
		if err := decodeWorkJSON(request, &input); err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		value, err = service.RemoveMembership(request.Context(), workmanagement.RemoveMembershipRequest{ProjectID: projectID, RepositoryID: parts[2], ExpectedVersion: expected, ExpectedMembershipRevision: input.ExpectedMembershipRevision}, key)
	} else {
		var input RepositoryMembershipInput
		if err := decodeWorkJSON(request, &input); err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		command := workmanagement.MembershipRequest{ProjectID: projectID, RepositoryPath: input.RepositoryPath, Label: input.Label, Role: input.Role, Settings: input.Settings, ExpectedVersion: expected, ExpectedMembershipRevision: input.ExpectedMembershipRevision}
		if len(parts) == 3 {
			if input.RepositoryPath != "" {
				writeWorkError(response, requestID, fmtWorkInvalid("editing a membership cannot change repository identity"))
				return
			}
			command.RepositoryID = parts[2]
		} else {
			command.RepositoryPath, err = existingAbsoluteDirectory(input.RepositoryPath)
			if err != nil {
				writeWorkError(response, requestID, err)
				return
			}
		}
		value, err = service.SetMembership(request.Context(), command, key)
	}
	if err != nil {
		writeWorkError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}
