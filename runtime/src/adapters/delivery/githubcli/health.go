package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

type repositoryResponse struct {
	DefaultBranch string `json:"default_branch"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Permissions   struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

func (adapter *Adapter) ProbeHealth(ctx context.Context, request delivery.HealthRequest) (delivery.HealthObservation, error) {
	if adapter == nil || adapter.runner == nil || adapter.now == nil {
		return delivery.HealthObservation{}, failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	observation := delivery.HealthObservation{ObservedAt: adapter.now().UTC(), Diagnostics: []string{}}
	if !filepath.IsAbs(request.LocalRepository) || !validRepositoryComponent(request.RemoteName) {
		return delivery.HealthObservation{}, failure(ports.FailureInvalidRequest, "health probe requires an absolute local repository and valid remote name", false)
	}
	remoteResult := adapter.execute(ctx, adapter.gitExecutable, []string{"-C", filepath.Clean(request.LocalRepository), "remote", "get-url", request.RemoteName}, nil)
	if remoteResult.err != nil {
		if ctx.Err() != nil {
			return delivery.HealthObservation{}, normalizeCommandFailure(ctx, remoteResult.err)
		}
		var classified *ports.Failure
		if errors.As(remoteResult.err, &classified) {
			return delivery.HealthObservation{}, classified
		}
		return delivery.HealthObservation{}, failure(ports.FailureNotFound, "configured Git remote could not be resolved", false)
	}
	repository, err := repositoryFromRemoteURL(strings.TrimSpace(string(remoteResult.stdout)))
	if err != nil {
		return delivery.HealthObservation{}, err
	}
	observation.Remote = delivery.Remote{Name: request.RemoteName}
	observation.Repository = repository

	auth := adapter.execute(ctx, adapter.executable, []string{"auth", "status", "--hostname", repository.Host, "--active"}, nil)
	if auth.err != nil {
		if ctx.Err() != nil {
			return delivery.HealthObservation{}, normalizeCommandFailure(ctx, auth.err)
		}
		var classified *ports.Failure
		if errors.As(auth.err, &classified) {
			return delivery.HealthObservation{}, classified
		}
		observation.Outcome = delivery.HealthUnauthenticated{Reason: "Authenticate GitHub CLI for " + repository.Host + "."}
		return observation, nil
	}
	accountResult := adapter.execute(ctx, adapter.executable, []string{"api", "--hostname", repository.Host, "user", "--jq", ".login"}, nil)
	if accountResult.err != nil {
		if ctx.Err() != nil {
			return delivery.HealthObservation{}, normalizeCommandFailure(ctx, accountResult.err)
		}
		var classified *ports.Failure
		if errors.As(accountResult.err, &classified) {
			return delivery.HealthObservation{}, classified
		}
		observation.Outcome = delivery.HealthUnauthenticated{Reason: "GitHub authentication could not resolve the active account."}
		return observation, nil
	}
	observation.Account = strings.TrimSpace(string(accountResult.stdout))
	if observation.Account == "" {
		return delivery.HealthObservation{}, failure(ports.FailureProtocolDrift, "GitHub account response did not contain a login", false)
	}
	if expected := strings.TrimSpace(request.Account); expected != "" && !strings.EqualFold(expected, observation.Account) {
		observation.Outcome = delivery.HealthDegraded{Reason: fmt.Sprintf("Authenticate GitHub CLI as %s before publishing.", expected)}
		return observation, nil
	}

	result := adapter.execute(ctx, adapter.executable, []string{"api", "--hostname", repository.Host, repositoryAPIPath(repository), "--method", "GET"}, nil)
	if result.err != nil {
		if ctx.Err() != nil {
			return delivery.HealthObservation{}, normalizeCommandFailure(ctx, result.err)
		}
		var classified *ports.Failure
		if errors.As(result.err, &classified) {
			return delivery.HealthObservation{}, classified
		}
		observation.Outcome = delivery.HealthUnavailable{Reason: "Verify the remote repository exists and the active account can read it."}
		return observation, nil
	}
	var response repositoryResponse
	if err := json.Unmarshal(result.stdout, &response); err != nil {
		return delivery.HealthObservation{}, failure(ports.FailureProtocolDrift, "GitHub repository response was invalid", false)
	}
	owner, name, ok := strings.Cut(response.FullName, "/")
	if !ok || owner == "" || name == "" || strings.TrimSpace(response.DefaultBranch) == "" {
		return delivery.HealthObservation{}, failure(ports.FailureProtocolDrift, "GitHub repository response was incomplete", false)
	}
	observation.Repository = delivery.Repository{Provider: Provider, Host: repository.Host, Owner: owner, Name: name}
	observation.BaseBranch = delivery.BranchRef{Repository: observation.Repository, Name: response.DefaultBranch}
	observation.EvidenceRef = strings.TrimSpace(response.HTMLURL)
	if observation.EvidenceRef == "" {
		observation.EvidenceRef = "https://" + repository.Host + "/" + owner + "/" + name
	}
	if !response.Permissions.Push {
		observation.Outcome = delivery.HealthReadOnly{Reason: "Grant the active GitHub account push permission or configure a writable fork remote."}
		return observation, nil
	}
	observation.Outcome = delivery.HealthReady{}
	return observation, nil
}

func repositoryFromRemoteURL(value string) (delivery.Repository, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return delivery.Repository{}, failure(ports.FailureInvalidRequest, "Git remote URL is empty", false)
	}
	host, repositoryPath := "", ""
	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return delivery.Repository{}, failure(ports.FailureInvalidRequest, "Git remote URL is invalid", false)
		}
		host, repositoryPath = parsed.Hostname(), parsed.Path
	} else if at := strings.LastIndex(trimmed, "@"); at >= 0 {
		if colon := strings.Index(trimmed[at+1:], ":"); colon >= 0 {
			host = trimmed[at+1 : at+1+colon]
			repositoryPath = trimmed[at+1+colon+1:]
		}
	}
	repositoryPath = strings.TrimSuffix(strings.Trim(path.Clean("/"+repositoryPath), "/"), ".git")
	parts := strings.Split(repositoryPath, "/")
	if host == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return delivery.Repository{}, failure(ports.FailureInvalidRequest, "Git remote must identify one GitHub owner and repository", false)
	}
	return delivery.Repository{Provider: Provider, Host: strings.ToLower(host), Owner: parts[0], Name: parts[1]}, nil
}

func repositoryAPIPath(repository delivery.Repository) string {
	return "repos/" + url.PathEscape(repository.Owner) + "/" + url.PathEscape(repository.Name)
}

func normalizedRepository(repository delivery.Repository) delivery.Repository {
	repository.Provider = Provider
	repository.Host = strings.ToLower(strings.TrimSpace(repository.Host))
	repository.Owner = strings.TrimSpace(repository.Owner)
	repository.Name = strings.TrimSuffix(strings.TrimSpace(repository.Name), ".git")
	return repository
}

func validateRepository(repository delivery.Repository) error {
	if strings.TrimSpace(repository.Provider) != "" && !strings.EqualFold(strings.TrimSpace(repository.Provider), Provider) {
		return failure(ports.FailureInvalidRequest, "repository provider must be GitHub", false)
	}
	repository = normalizedRepository(repository)
	if !validHost(repository.Host) || !validRepositoryComponent(repository.Owner) || !validRepositoryComponent(repository.Name) {
		return failure(ports.FailureInvalidRequest, "GitHub repository coordinates are invalid", false)
	}
	return nil
}

func validRefName(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") || strings.HasSuffix(trimmed, ".") {
		return false
	}
	if strings.ContainsAny(trimmed, " ~^:?*[\\\t\r\n") || strings.Contains(trimmed, "..") || strings.Contains(trimmed, "@{") || strings.Contains(trimmed, "//") {
		return false
	}
	for _, component := range strings.Split(trimmed, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validHost(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func validRepositoryComponent(value string) bool {
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, "-") || strings.Contains(value, "/") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
