// Package linear implements read-only Linear ticket observation through its
// public GraphQL API. It cannot write issues, select workflows or create runs.
package linear

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/trackerconnection"
)

type Authentication string

const (
	PersonalAPIKey Authentication = "personal_api_key"
	OAuth          Authentication = "oauth"
)

// Config contains only non-secret configuration and a protected credential
// reference. Empty account/workspace/team permit bootstrap scope discovery only.
type Config struct {
	Endpoint                                                  string
	InstallationID, AccountID, WorkspaceID, TeamID, ProjectID string
	BindingRevision, ConfigRevision, CredentialRef            string
	Authentication                                            Authentication
}

type Adapter struct {
	config      Config
	client      *http.Client
	credentials trackerconnection.CredentialResolver
	evidence    trackerconnection.EvidenceStore
	pin         tracker.Pin
	scope       tracker.Scope
	now         func() time.Time
}

func New(config Config, client *http.Client, credentials trackerconnection.CredentialResolver, evidence trackerconnection.EvidenceStore) (*Adapter, error) {
	if config.Endpoint == "" {
		config.Endpoint = "https://api.linear.app/graphql"
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fail(ports.FailureInvalidRequest, "Linear endpoint must be the official HTTPS API or an explicit loopback test endpoint")
	}
	production := endpoint.Scheme == "https" && endpoint.Host == "api.linear.app" && endpoint.Path == "/graphql"
	loopback := endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost" || endpoint.Hostname() == "::1")
	if !production && !loopback {
		return nil, fail(ports.FailureInvalidRequest, "Linear endpoint must be the official HTTPS API or an explicit loopback test endpoint")
	}
	if credentials == nil || evidence == nil || config.InstallationID == "" || config.CredentialRef == "" || config.BindingRevision == "" || config.ConfigRevision == "" {
		return nil, fail(ports.FailureInvalidRequest, "Linear requires protected credentials, evidence storage and versioned installation configuration")
	}
	if config.Authentication != PersonalAPIKey && config.Authentication != OAuth {
		return nil, fail(ports.FailureUnsupported, "Linear authentication must be personal_api_key or oauth")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	privateClient := *client
	privateClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if privateClient.Timeout == 0 {
		privateClient.Timeout = 30 * time.Second
	}
	// Credential reference is a locator, not authority/configuration. Rotation
	// may preserve the pin, while every response verifies viewer/workspace IDs.
	pinned := config
	pinned.CredentialRef = ""
	scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: config.WorkspaceID, ScopeID: config.WorkspaceID}, ContainerID: config.TeamID}
	pin := tracker.Pin{AdapterConfigPin: tracker.AdapterConfigPin{ContractVersion: tracker.Version, AdapterID: "linear", AdapterVersion: "1", InstallationID: config.InstallationID, AccountID: config.AccountID, BindingRevision: config.BindingRevision, ConfigRevision: config.ConfigRevision, ConfigDigest: digestJSON(pinned)}, CapabilitiesDigest: digestJSON("linear-readonly-capabilities/v1")}
	return &Adapter{config: config, client: &privateClient, credentials: credentials, evidence: evidence, pin: pin, scope: scope, now: time.Now}, nil
}

func (a *Adapter) ConfigPin() tracker.AdapterConfigPin {
	return a.pin.AdapterConfigPin
}

func (a *Adapter) Scope() tracker.Scope {
	return a.scope
}

type graphError struct {
	Path       []any `json:"path"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type identityData struct {
	Viewer       tracker.NamedID `json:"viewer"`
	Organization tracker.NamedID `json:"organization"`
}

const authoritySelection = `viewer { id name } organization { id name }`

func (a *Adapter) query(ctx context.Context, operation string, variables map[string]any, destination any) ([]byte, error) {
	credential, err := a.credentials.Resolve(ctx, a.config.CredentialRef)
	if err != nil || strings.TrimSpace(credential) == "" || strings.ContainsAny(credential, "\r\n") {
		return nil, fail(ports.FailureUnauthenticated, "Linear credential reference could not be resolved")
	}
	body, err := json.Marshal(map[string]any{"query": operation, "variables": variables})
	if err != nil {
		return nil, fail(ports.FailureInvalidRequest, "Linear query variables are invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fail(ports.FailureInvalidRequest, "Linear query request is invalid")
	}
	authorization := credential
	if a.config.Authentication == OAuth {
		authorization = "Bearer " + credential
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, fail(ports.FailureUnavailable, "Linear connection failed")
	}
	defer func() {
		_ = response.Body.Close()
	}()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil || len(raw) > 8*1024*1024 {
		return nil, fail(ports.FailureResourceExhausted, "Linear response exceeded the supported read limit")
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphError    `json:"errors"`
	}
	decoded := json.Unmarshal(raw, &envelope) == nil
	if response.StatusCode == http.StatusUnauthorized {
		return nil, fail(ports.FailureUnauthenticated, "Linear credentials are invalid or revoked")
	}
	if response.StatusCode == http.StatusForbidden {
		return nil, fail(ports.FailurePermissionDenied, "Linear account cannot access this resource")
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, rateLimit(response.Header, a.now())
	}
	for _, item := range envelope.Errors {
		if item.Extensions.Code == "RATELIMITED" {
			return nil, rateLimit(response.Header, a.now())
		}
	}
	if len(envelope.Errors) != 0 {
		// Reject the entire partial response. Missing fields must never become
		// observed-empty values or successful refreshes.
		code := ports.FailureUnavailable
		for _, item := range envelope.Errors {
			switch item.Extensions.Code {
			case "UNAUTHENTICATED", "AUTHENTICATION_ERROR":
				code = ports.FailureUnauthenticated
			case "FORBIDDEN", "PERMISSION_DENIED":
				code = ports.FailurePermissionDenied
			case "GRAPHQL_VALIDATION_FAILED":
				code = ports.FailureProtocolDrift
			case "ENTITY_NOT_FOUND", "NOT_FOUND":
				var identity identityData
				if len(envelope.Errors) == 1 && len(item.Path) == 1 && item.Path[0] == "issue" && json.Unmarshal(envelope.Data, &identity) == nil && identity.Viewer.ID == a.config.AccountID && identity.Organization.ID == a.config.WorkspaceID && a.config.AccountID != "" && a.config.WorkspaceID != "" {
					code = ports.FailureNotFound
				}
			}
			if code == ports.FailureUnauthenticated || code == ports.FailurePermissionDenied {
				break
			}
		}
		return nil, fail(code, "Linear did not return a complete authorized observation")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fail(ports.FailureUnavailable, "Linear API request was not successful")
	}
	if !decoded || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, fail(ports.FailureProtocolDrift, "Linear returned an invalid observation envelope")
	}
	var authority identityData
	if json.Unmarshal(envelope.Data, &authority) != nil || authority.Viewer.ID == "" || authority.Organization.ID == "" {
		return nil, fail(ports.FailureProtocolDrift, "Linear response lacks stable account and workspace identity")
	}
	if a.config.AccountID != "" && authority.Viewer.ID != a.config.AccountID || a.config.WorkspaceID != "" && authority.Organization.ID != a.config.WorkspaceID {
		return nil, fail(ports.FailureProtocolDrift, "Linear credential authority differs from the selected account or workspace")
	}
	if err := json.Unmarshal(envelope.Data, destination); err != nil {
		return nil, fail(ports.FailureProtocolDrift, "Linear response does not match its observed schema")
	}
	return raw, nil
}

func (a *Adapter) retain(ctx context.Context, kind, sourceURL string, raw []byte) (string, error) {
	envelope := struct {
		Version    string    `json:"version"`
		Provider   string    `json:"provider"`
		Kind       string    `json:"kind"`
		SourceURL  string    `json:"sourceURL"`
		MediaType  string    `json:"mediaType"`
		ObservedAt time.Time `json:"observedAt"`
		BodySHA256 string    `json:"bodySHA256"`
		Body       string    `json:"body"`
	}{"darkstar.source-evidence/v1", "linear", kind, sourceURL, "application/json", a.now().UTC(), digestBytes(raw), string(raw)}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fail(ports.FailureProtocolDrift, "Linear source evidence could not be encoded")
	}
	ref, err := a.evidence.Retain(ctx, encoded)
	if err != nil || ref == "" {
		return "", fail(ports.FailureUnavailable, "Linear source evidence could not be retained")
	}
	return ref, nil
}

func rateLimit(header http.Header, now time.Time) error {
	details := make(map[string]string)
	if seconds, err := strconv.ParseInt(header.Get("Retry-After"), 10, 64); err == nil && seconds >= 0 {
		details["retry_after_seconds"] = strconv.FormatInt(seconds, 10)
	} else if value, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		details["retry_at_utc"] = value.UTC().Format(time.RFC3339)
	}
	var reset time.Time
	for _, key := range []string{"X-RateLimit-Requests-Reset", "X-RateLimit-Endpoint-Requests-Reset", "X-RateLimit-Complexity-Reset"} {
		if epoch, err := strconv.ParseInt(header.Get(key), 10, 64); err == nil {
			candidate := time.UnixMilli(epoch)
			if candidate.After(now) && candidate.After(reset) {
				reset = candidate
			}
		}
	}
	if !reset.IsZero() {
		details["retry_at_utc"] = reset.UTC().Format(time.RFC3339Nano)
	}
	return &ports.Failure{Code: ports.FailureResourceExhausted, Message: "Linear rate limit reached", Retryable: true, Details: details}
}

func fail(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message, Retryable: code == ports.FailureUnavailable}
}

func isFailure(err error, code ports.FailureCode) bool {
	var failure *ports.Failure
	return errors.As(err, &failure) && failure.Code == code
}

func digestBytes(value []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func digestJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return digestBytes(encoded)
}
