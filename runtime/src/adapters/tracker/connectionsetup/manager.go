// Package connectionsetup composes protected local configuration with provider
// account discovery. It grants no writer, project binding, or execution authority.
package connectionsetup

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"

	"darkstar/src/adapters/tracker/githubissues"
	"darkstar/src/adapters/tracker/linear"
	"darkstar/src/adapters/tracker/localconnection"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/trackerconnection"
	"darkstar/src/ports/worksource"
)

type Options struct {
	HTTPClient   *http.Client
	GHExecutable string
}

type Manager struct {
	registry    *localconnection.Registry
	credentials *localconnection.Credentials
	evidence    *localconnection.EvidenceStore
	options     Options
}

func New(root string, options Options) (*Manager, error) {
	registry, err := localconnection.NewRegistry(filepath.Join(root, "connections"))
	if err != nil {
		return nil, err
	}
	credentials, err := localconnection.NewCredentials(filepath.Join(root, "credentials"))
	if err != nil {
		return nil, err
	}
	evidence, err := localconnection.NewEvidenceStore(filepath.Join(root, "evidence"))
	if err != nil {
		return nil, err
	}
	return &Manager{registry: registry, credentials: credentials, evidence: evidence, options: options}, nil
}

func (m *Manager) StoreCredential(ctx context.Context, ref, secret string) error {
	return m.credentials.Put(ctx, ref, secret)
}

func (m *Manager) List(ctx context.Context) ([]trackerconnection.Record, error) {
	return m.registry.List(ctx)
}

func (m *Manager) Get(ctx context.Context, id, revision string) (trackerconnection.Record, error) {
	return m.registry.Get(ctx, id, revision)
}

func (m *Manager) CreateLinear(ctx context.Context, request trackerconnection.LinearSetupRequest) (trackerconnection.Record, error) {
	return m.create(ctx, request.SchemaVersion, request.ConnectionID, request.Revision, trackerconnection.LinearConfiguration{CredentialRef: request.CredentialRef, Authentication: request.Authentication})
}

func (m *Manager) CreateGitHubToken(ctx context.Context, request trackerconnection.GitHubTokenSetupRequest) (trackerconnection.Record, error) {
	return m.create(ctx, request.SchemaVersion, request.ConnectionID, request.Revision, trackerconnection.GitHubTokenConfiguration{Host: request.Host, CredentialRef: request.CredentialRef})
}

func (m *Manager) CreateGitHubCLI(ctx context.Context, request trackerconnection.GitHubCLISetupRequest) (trackerconnection.Record, error) {
	return m.create(ctx, request.SchemaVersion, request.ConnectionID, request.Revision, trackerconnection.GitHubCLIConfiguration{Host: request.Host, Login: request.Login})
}

func withoutAuthority(configuration trackerconnection.Configuration) trackerconnection.Configuration {
	switch value := configuration.(type) {
	case trackerconnection.LinearConfiguration:
		value.AccountID = ""
		value.WorkspaceID = ""
		return value
	case trackerconnection.GitHubTokenConfiguration:
		value.AccountID = ""
		return value
	case trackerconnection.GitHubCLIConfiguration:
		value.AccountID = ""
		return value
	default:
		return nil
	}
}

func (m *Manager) create(ctx context.Context, version int, id, revision string, configuration trackerconnection.Configuration) (trackerconnection.Record, error) {
	if err := localconnection.ValidateConnectionIdentity(version, id, revision); err != nil {
		return trackerconnection.Record{}, err
	}
	prior, err := m.registry.Get(ctx, id, revision)
	if err == nil {
		if !reflect.DeepEqual(withoutAuthority(prior.Configuration), configuration) {
			return trackerconnection.Record{}, fail(ports.FailureConflict, "connection revision already exists; choose a new revision")
		}
		return prior, nil
	}
	var problem *ports.Failure
	if !errors.As(err, &problem) || problem.Code != ports.FailureNotFound {
		return trackerconnection.Record{}, err
	}
	record := trackerconnection.Record{SchemaVersion: 1, ConnectionID: id, Revision: revision, Configuration: configuration}
	health, err := m.observeHealth(ctx, record)
	if err != nil {
		return trackerconnection.Record{}, err
	}
	if health.Account.ID == "" || health.EvidenceRef == "" || health.ObservedAt.IsZero() {
		return trackerconnection.Record{}, fail(ports.FailureProtocolDrift, "account bootstrap omitted identity or evidence")
	}
	switch value := configuration.(type) {
	case trackerconnection.LinearConfiguration:
		if health.Workspace == nil || health.Workspace.ID == "" {
			return trackerconnection.Record{}, fail(ports.FailureProtocolDrift, "Linear bootstrap omitted workspace identity")
		}
		value.AccountID = health.Account.ID
		value.WorkspaceID = health.Workspace.ID
		record.Configuration = value
	case trackerconnection.GitHubTokenConfiguration:
		value.AccountID = health.Account.ID
		record.Configuration = value
	case trackerconnection.GitHubCLIConfiguration:
		value.AccountID = health.Account.ID
		record.Configuration = value
	default:
		return trackerconnection.Record{}, fail(ports.FailureUnsupported, "unsupported tracker connection")
	}
	record.AccountName = health.Account.Name
	record.ObservedAt = health.ObservedAt
	record.EvidenceRef = health.EvidenceRef
	return m.registry.Publish(ctx, record)
}

func (m *Manager) linear(record trackerconnection.Record, config trackerconnection.LinearConfiguration) (*linear.Adapter, error) {
	return linear.New(linear.Config{InstallationID: record.ConnectionID, AccountID: config.AccountID, WorkspaceID: config.WorkspaceID, BindingRevision: "connection", ConfigRevision: record.Revision, CredentialRef: config.CredentialRef, Authentication: linear.Authentication(config.Authentication)}, m.options.HTTPClient, m.credentials, m.evidence)
}

func (m *Manager) github(record trackerconnection.Record) (githubissues.Config, githubissues.Options, error) {
	config := githubissues.Config{InstallationID: record.ConnectionID, ConfigRevision: record.Revision, BindingRevision: "connection"}
	options := githubissues.Options{HTTPClient: m.options.HTTPClient, Evidence: m.evidence}
	switch value := record.Configuration.(type) {
	case trackerconnection.GitHubTokenConfiguration:
		config.Host = value.Host
		config.AccountID = value.AccountID
		config.CredentialRef = value.CredentialRef
		options.Credentials = m.credentials
	case trackerconnection.GitHubCLIConfiguration:
		config.Host = value.Host
		config.AccountID = value.AccountID
		config.CredentialRef = "gh:" + record.ConnectionID
		credentials, err := githubissues.NewGHCredentialResolver(m.options.GHExecutable, map[string]githubissues.GHCredential{config.CredentialRef: {Host: value.Host, Login: value.Login}})
		if err != nil {
			return githubissues.Config{}, githubissues.Options{}, err
		}
		options.Credentials = credentials
	default:
		return githubissues.Config{}, githubissues.Options{}, fail(ports.FailureUnsupported, "connection is not a GitHub account")
	}
	return config, options, nil
}

func (m *Manager) observeHealth(ctx context.Context, record trackerconnection.Record) (trackerconnection.Health, error) {
	if config, ok := record.Configuration.(trackerconnection.LinearConfiguration); ok {
		adapter, err := m.linear(record, config)
		if err != nil {
			return trackerconnection.Health{}, err
		}
		health, err := adapter.Health(ctx)
		if err != nil {
			return trackerconnection.Health{}, err
		}
		return trackerconnection.Health{SchemaVersion: 1, Account: named(health.Account), Workspace: &trackerconnection.NamedID{ID: health.Workspace.ID, Name: health.Workspace.Name}, ObservedAt: health.ObservedAt, EvidenceRef: health.EvidenceRef}, nil
	}
	config, options, err := m.github(record)
	if err != nil {
		return trackerconnection.Health{}, err
	}
	connection, err := githubissues.NewConnection(config, options)
	if err != nil {
		return trackerconnection.Health{}, err
	}
	health, err := connection.ProbeHealth(ctx)
	if err != nil {
		return trackerconnection.Health{}, err
	}
	return trackerconnection.Health{SchemaVersion: 1, Account: named(health.Account), ObservedAt: health.ObservedAt, EvidenceRef: health.EvidenceRef}, nil
}

func (m *Manager) Health(ctx context.Context, id, revision string) (trackerconnection.Health, error) {
	record, err := m.registry.Get(ctx, id, revision)
	if err != nil {
		return trackerconnection.Health{}, err
	}
	return m.observeHealth(ctx, record)
}

func (m *Manager) Destinations(ctx context.Context, id, revision, cursor string, pageSize int) (trackerconnection.Destinations, error) {
	record, err := m.registry.Get(ctx, id, revision)
	if err != nil {
		return trackerconnection.Destinations{}, err
	}
	if pageSize < 1 || pageSize > 100 {
		return trackerconnection.Destinations{}, fail(ports.FailureInvalidRequest, "discovery page size must be 1 to 100")
	}
	if config, ok := record.Configuration.(trackerconnection.LinearConfiguration); ok {
		if cursor != "" {
			return trackerconnection.Destinations{}, fail(ports.FailureUnsupported, "Linear scope discovery returns one complete result without a continuation cursor")
		}
		adapter, err := m.linear(record, config)
		if err != nil {
			return trackerconnection.Destinations{}, err
		}
		observation, err := adapter.DiscoverScopes(ctx)
		if err != nil {
			return trackerconnection.Destinations{}, err
		}
		result := trackerconnection.Destinations{SchemaVersion: 1, Destinations: []trackerconnection.Destination{}, ObservedAt: observation.ObservedAt, EvidenceRefs: observation.EvidenceRefs}
		for _, team := range observation.Teams {
			destination := trackerconnection.Destination{Provider: "linear", Host: "linear.app", TenantID: config.WorkspaceID, ScopeID: config.WorkspaceID, ContainerID: team.Team.ID, Name: team.Team.Name, Projects: []trackerconnection.NamedID{}}
			for _, project := range team.Projects {
				destination.Projects = append(destination.Projects, named(project))
			}
			result.Destinations = append(result.Destinations, destination)
		}
		return result, nil
	}
	config, options, err := m.github(record)
	if err != nil {
		return trackerconnection.Destinations{}, err
	}
	connection, err := githubissues.NewConnection(config, options)
	if err != nil {
		return trackerconnection.Destinations{}, err
	}
	page, err := connection.DiscoverDestinations(ctx, cursor, pageSize)
	if err != nil {
		return trackerconnection.Destinations{}, err
	}
	result := trackerconnection.Destinations{SchemaVersion: 1, Destinations: []trackerconnection.Destination{}, ObservedAt: page.ObservedAt, EvidenceRefs: []string{page.EvidenceRef}}
	for _, value := range page.Destinations {
		result.Destinations = append(result.Destinations, trackerconnection.Destination{Provider: value.Scope.Namespace.Provider, Host: value.Scope.Namespace.Host, TenantID: value.Scope.Namespace.TenantID, ScopeID: value.Scope.Namespace.ScopeID, ContainerID: value.Scope.ContainerID, Name: value.Name, URL: value.URL, Projects: []trackerconnection.NamedID{}})
	}
	switch next := page.Next.(type) {
	case tracker.End:
	case tracker.More:
		result.NextCursor = next.Cursor
	default:
		return trackerconnection.Destinations{}, fail(ports.FailureProtocolDrift, "unknown discovery continuation")
	}
	return result, nil
}

type SourceBinding struct {
	Source  worksource.TrackerSourceV1
	Browser worksource.TrackerBrowserV1
	Config  tracker.AdapterConfigPin
}

// Resolve constructs only the exact immutable connection/scope selected by the
// daemon. It performs no browse, project binding, publication or scheduling.
func (m *Manager) ResolveSource(ctx context.Context, id, revision string, scope tracker.Scope, bindingRevision string) (SourceBinding, error) {
	if strings.TrimSpace(bindingRevision) == "" {
		return SourceBinding{}, fail(ports.FailureInvalidRequest, "source resolution requires an exact binding revision")
	}
	record, err := m.registry.Get(ctx, id, revision)
	if err != nil {
		return SourceBinding{}, err
	}
	if config, ok := record.Configuration.(trackerconnection.LinearConfiguration); ok {
		if scope.Namespace != (tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: config.WorkspaceID, ScopeID: config.WorkspaceID}) || scope.ContainerID == "" {
			return SourceBinding{}, fail(ports.FailureConflict, "Linear source scope differs from the pinned connection authority")
		}
		adapter, err := linear.New(linear.Config{InstallationID: id, ConfigRevision: revision, BindingRevision: bindingRevision, AccountID: config.AccountID, WorkspaceID: config.WorkspaceID, TeamID: scope.ContainerID, CredentialRef: config.CredentialRef, Authentication: linear.Authentication(config.Authentication)}, m.options.HTTPClient, m.credentials, m.evidence)
		if err != nil {
			return SourceBinding{}, err
		}
		return SourceBinding{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
	}
	config, options, err := m.github(record)
	if err != nil {
		return SourceBinding{}, err
	}
	if scope.Namespace.Provider != "github_issues" || scope.Namespace.Host != config.Host || scope.Namespace.ScopeID != scope.ContainerID || scope.ContainerID == "" || scope.Namespace.TenantID == "" {
		return SourceBinding{}, fail(ports.FailureConflict, "GitHub source scope differs from the pinned connection")
	}
	config.BindingRevision = bindingRevision
	config.TenantID = scope.Namespace.TenantID
	config.RepositoryID = scope.ContainerID
	adapter, err := githubissues.New(config, options)
	if err != nil {
		return SourceBinding{}, err
	}
	return SourceBinding{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
}

func named(value tracker.NamedID) trackerconnection.NamedID {
	return trackerconnection.NamedID{ID: value.ID, Name: value.Name}
}

func fail(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}
