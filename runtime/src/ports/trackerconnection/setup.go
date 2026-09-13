package trackerconnection

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

type Configuration interface {
	isConnectionConfiguration()
}

type LinearConfiguration struct {
	CredentialRef  string `json:"credentialRef"`
	Authentication string `json:"authentication"`
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
}

func (LinearConfiguration) isConnectionConfiguration() {}

type GitHubTokenConfiguration struct {
	Host          string `json:"host"`
	CredentialRef string `json:"credentialRef"`
	AccountID     string `json:"accountId"`
}

func (GitHubTokenConfiguration) isConnectionConfiguration() {}

type GitHubCLIConfiguration struct {
	Host      string `json:"host"`
	Login     string `json:"login"`
	AccountID string `json:"accountId"`
}

func (GitHubCLIConfiguration) isConnectionConfiguration() {}

// Record is an immutable account configuration. Source scope and project binding
// are independent. The codec tags provider/authentication variants explicitly.
type Record struct {
	SchemaVersion int
	ConnectionID  string
	Revision      string
	Configuration Configuration
	AccountName   string
	ObservedAt    time.Time
	EvidenceRef   string
}

type recordWire struct {
	SchemaVersion int             `json:"schemaVersion"`
	ConnectionID  string          `json:"connectionId"`
	Revision      string          `json:"revision"`
	Kind          string          `json:"kind"`
	Configuration json.RawMessage `json:"configuration"`
	AccountName   string          `json:"accountName"`
	ObservedAt    time.Time       `json:"observedAt"`
	EvidenceRef   string          `json:"evidenceRef"`
}

func (r Record) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	wire := recordWire{SchemaVersion: r.SchemaVersion, ConnectionID: r.ConnectionID, Revision: r.Revision, AccountName: r.AccountName, ObservedAt: r.ObservedAt, EvidenceRef: r.EvidenceRef}
	switch r.Configuration.(type) {
	case LinearConfiguration:
		wire.Kind = "linear"
	case GitHubTokenConfiguration:
		wire.Kind = "github_token"
	case GitHubCLIConfiguration:
		wire.Kind = "github_cli"
	default:
		return nil, errors.New("unsupported tracker connection configuration")
	}
	configuration, err := json.Marshal(r.Configuration)
	if err != nil {
		return nil, err
	}
	wire.Configuration = configuration
	return json.Marshal(wire)
}

func (r *Record) UnmarshalJSON(content []byte) error {
	var wire recordWire
	if err := strictJSON(content, &wire); err != nil {
		return err
	}
	if wire.SchemaVersion != 1 {
		return errors.New("unsupported tracker connection version")
	}
	var configuration Configuration
	switch wire.Kind {
	case "linear":
		var value LinearConfiguration
		if err := strictJSON(wire.Configuration, &value); err != nil {
			return err
		}
		configuration = value
	case "github_token":
		var value GitHubTokenConfiguration
		if err := strictJSON(wire.Configuration, &value); err != nil {
			return err
		}
		configuration = value
	case "github_cli":
		var value GitHubCLIConfiguration
		if err := strictJSON(wire.Configuration, &value); err != nil {
			return err
		}
		configuration = value
	default:
		return errors.New("unsupported tracker connection kind")
	}
	*r = Record{SchemaVersion: wire.SchemaVersion, ConnectionID: wire.ConnectionID, Revision: wire.Revision, Configuration: configuration, AccountName: wire.AccountName, ObservedAt: wire.ObservedAt, EvidenceRef: wire.EvidenceRef}
	return r.Validate()
}

func (r Record) Validate() error {
	if r.SchemaVersion != 1 || r.ConnectionID == "" || r.Revision == "" || r.AccountName == "" || r.ObservedAt.IsZero() || r.EvidenceRef == "" {
		return errors.New("tracker connection requires retained account observation")
	}
	switch config := r.Configuration.(type) {
	case LinearConfiguration:
		if config.AccountID == "" || config.WorkspaceID == "" || config.CredentialRef == "" || (config.Authentication != "personal_api_key" && config.Authentication != "oauth") {
			return errors.New("linear connection requires exact account, workspace and authentication")
		}
	case GitHubTokenConfiguration:
		if config.AccountID == "" || config.CredentialRef == "" || config.Host == "" {
			return errors.New("GitHub token connection requires exact account, host and credential reference")
		}
	case GitHubCLIConfiguration:
		if config.AccountID == "" || config.Host == "" || config.Login == "" {
			return errors.New("GitHub CLI connection requires exact account, host and login")
		}
	default:
		return errors.New("unsupported tracker connection configuration")
	}
	return nil
}

func strictJSON(content []byte, result any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing tracker connection JSON")
	}
	return nil
}

type LinearSetupRequest struct {
	SchemaVersion  int    `json:"schemaVersion"`
	ConnectionID   string `json:"connectionId"`
	Revision       string `json:"revision"`
	CredentialRef  string `json:"credentialRef"`
	Authentication string `json:"authentication"`
}

type GitHubTokenSetupRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ConnectionID  string `json:"connectionId"`
	Revision      string `json:"revision"`
	Host          string `json:"host"`
	CredentialRef string `json:"credentialRef"`
}

type GitHubCLISetupRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ConnectionID  string `json:"connectionId"`
	Revision      string `json:"revision"`
	Host          string `json:"host"`
	Login         string `json:"login"`
}

type NamedID struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Health struct {
	SchemaVersion int       `json:"schemaVersion"`
	Account       NamedID   `json:"account"`
	Workspace     *NamedID  `json:"workspace,omitempty"`
	ObservedAt    time.Time `json:"observedAt"`
	EvidenceRef   string    `json:"evidenceRef"`
}

type Destination struct {
	Provider    string    `json:"provider"`
	Host        string    `json:"host"`
	TenantID    string    `json:"tenantId"`
	ScopeID     string    `json:"scopeId"`
	ContainerID string    `json:"containerId"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Projects    []NamedID `json:"projects"`
}

type Destinations struct {
	SchemaVersion int           `json:"schemaVersion"`
	Destinations  []Destination `json:"destinations"`
	NextCursor    string        `json:"nextCursor"`
	ObservedAt    time.Time     `json:"observedAt"`
	EvidenceRefs  []string      `json:"evidenceRefs"`
}
