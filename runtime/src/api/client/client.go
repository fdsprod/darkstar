// Package client implements discovery, autostart, version negotiation, and
// authenticated JSON transport for DARKSTAR CLI commands.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	localapi "github.com/fdsprod/darkstar/runtime/src/api"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

const (
	defaultTimeout  = 10 * time.Second
	maxResponseSize = 4 << 20
	maxDownloadSize = 64 << 20
)

// FailureKind is a closed transport failure classification. API business
// failures are represented separately by APIError.
type FailureKind string

const (
	FailureDiscovery    FailureKind = "discovery"
	FailureUnavailable  FailureKind = "unavailable"
	FailureIncompatible FailureKind = "incompatible"
	FailureProtocol     FailureKind = "protocol"
)

// Failure reports a client-side failure before a valid API error was received.
type Failure struct {
	Kind FailureKind
	Op   string
	Err  error
}

func (failure *Failure) Error() string {
	return fmt.Sprintf("%s: %v", failure.Op, failure.Err)
}

func (failure *Failure) Unwrap() error { return failure.Err }

// ErrorDetail identifies one field-level API failure.
type ErrorDetail struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// APIError is the stable error envelope returned by the local API.
type APIError struct {
	HTTPStatus      int           `json:"-"`
	SchemaVersion   int           `json:"schemaVersion"`
	Code            string        `json:"code"`
	Message         string        `json:"message"`
	RequestID       string        `json:"requestId"`
	Retryable       bool          `json:"retryable"`
	ResourceVersion *int64        `json:"resourceVersion,omitempty"`
	Details         []ErrorDetail `json:"details,omitempty"`
}

// RequestOption adds non-secret request metadata required by a command.
type RequestOption func(*http.Request)

// WithHeader adds one HTTP header to a JSON request.
func WithHeader(name, value string) RequestOption {
	return func(request *http.Request) { request.Header.Set(name, value) }
}

func (problem *APIError) Error() string {
	if problem.RequestID == "" {
		return fmt.Sprintf("%s: %s", problem.Code, problem.Message)
	}
	return fmt.Sprintf("%s: %s (request %s)", problem.Code, problem.Message, problem.RequestID)
}

// AutostartFunc starts or confirms the per-user daemon. Implementations must be
// idempotent because concurrent CLI commands can call it at the same time.
type AutostartFunc func(context.Context) error

// Config supplies the process-local dependencies for a Client.
type Config struct {
	RuntimeDirectory string
	HTTPClient       *http.Client
	Autostart        AutostartFunc
	Versions         []localapi.Version
}

// Client is a reusable, business-logic-free transport for CLI commands.
type Client struct {
	runtimeDirectory string
	http             *http.Client
	autostart        AutostartFunc
	versions         []localapi.Version
}

// New validates a client configuration.
func New(config Config) (*Client, error) {
	if config.RuntimeDirectory == "" {
		return nil, errors.New("API client runtime directory is required")
	}
	if !filepath.IsAbs(config.RuntimeDirectory) {
		return nil, errors.New("API client runtime directory must be absolute")
	}
	versions := append([]localapi.Version(nil), config.Versions...)
	if len(versions) == 0 {
		versions = localapi.SupportedVersions()
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		runtimeDirectory: config.RuntimeDirectory,
		http:             httpClient,
		autostart:        config.Autostart,
		versions:         versions,
	}, nil
}

// Session is one discovered and version-negotiated daemon connection.
type Session struct {
	client   *Client
	endpoint localapi.Endpoint
	version  localapi.Version
	recovery localapi.RecoveryStatus
}

// Version returns the negotiated API representation version.
func (session *Session) Version() localapi.Version { return session.version }

// Recovery returns the daemon's safe startup-reconciliation summary.
func (session *Session) Recovery() localapi.RecoveryStatus { return session.recovery }

// EndpointMetadata is the safe subset of endpoint discovery state exposed to
// command output. It cannot represent or serialize the bearer credential.
type EndpointMetadata struct {
	APIVersion       localapi.Version
	PID              int
	ProcessStartedAt time.Time
	Port             int
	CreatedAt        time.Time
}

// Endpoint returns non-secret discovery metadata.
func (session *Session) Endpoint() EndpointMetadata {
	return EndpointMetadata{
		APIVersion:       session.endpoint.APIVersion,
		PID:              session.endpoint.PID,
		ProcessStartedAt: session.endpoint.ProcessStartedAt,
		Port:             session.endpoint.Port,
		CreatedAt:        session.endpoint.CreatedAt,
	}
}

// Connect discovers and authenticates the daemon. It retries once after
// idempotent autostart for missing, stale, or unreachable endpoint state.
func (client *Client) Connect(ctx context.Context) (*Session, error) {
	session, err := client.connectOnce(ctx)
	if err == nil || client.autostart == nil || !autostartCandidate(err) {
		return session, err
	}
	if startErr := client.autostart(ctx); startErr != nil {
		return nil, &Failure{Kind: FailureUnavailable, Op: "autostart daemon", Err: startErr}
	}
	return client.connectOnce(ctx)
}

func (client *Client) connectOnce(ctx context.Context) (*Session, error) {
	endpoint, err := localapi.ReadEndpoint(client.runtimeDirectory)
	if err != nil {
		kind := FailureDiscovery
		if !errors.Is(err, os.ErrNotExist) {
			kind = FailureUnavailable
		}
		return nil, &Failure{Kind: kind, Op: "discover daemon API", Err: err}
	}
	version, err := localapi.NegotiateVersion(endpoint, client.versions)
	if err != nil {
		return nil, &Failure{Kind: FailureIncompatible, Op: "negotiate daemon API", Err: err}
	}
	session := &Session{client: client, endpoint: endpoint, version: version}
	var root struct {
		SchemaVersion int                     `json:"schemaVersion"`
		APIVersion    localapi.Version        `json:"apiVersion"`
		Recovery      localapi.RecoveryStatus `json:"recovery"`
	}
	if err := session.DoJSON(ctx, http.MethodGet, "", nil, &root); err != nil {
		var problem *APIError
		if errors.As(err, &problem) && problem.HTTPStatus == http.StatusUnauthorized {
			return nil, &Failure{Kind: FailureUnavailable, Op: "authenticate daemon API", Err: err}
		}
		return nil, err
	}
	if root.SchemaVersion != 1 || root.APIVersion != version {
		return nil, &Failure{Kind: FailureProtocol, Op: "validate daemon API", Err: fmt.Errorf("unexpected root response schemaVersion=%d apiVersion=%q", root.SchemaVersion, root.APIVersion)}
	}
	session.recovery = root.Recovery
	return session, nil
}

func autostartCandidate(err error) bool {
	var failure *Failure
	return errors.As(err, &failure) && (failure.Kind == FailureDiscovery || failure.Kind == FailureUnavailable)
}

// DoJSON sends one authenticated API request within the negotiated version.
// Resource is relative to /api/<version>/ and cannot escape that prefix.
func (session *Session) DoJSON(ctx context.Context, method, resource string, requestBody, responseBody any, options ...RequestOption) error {
	resourceURL, err := session.resourceURL(resource)
	if err != nil {
		return &Failure{Kind: FailureProtocol, Op: "build API request", Err: err}
	}
	var body io.Reader
	if requestBody != nil {
		encoded, encodeErr := json.Marshal(requestBody)
		if encodeErr != nil {
			return &Failure{Kind: FailureProtocol, Op: "encode API request", Err: encodeErr}
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, resourceURL, body)
	if err != nil {
		return &Failure{Kind: FailureProtocol, Op: "build API request", Err: err}
	}
	request.Header.Set("Authorization", session.endpoint.AuthorizationHeader())
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, option := range options {
		if option != nil {
			option(request)
		}
	}

	response, err := session.client.http.Do(request)
	if err != nil {
		return &Failure{Kind: FailureUnavailable, Op: "call daemon API", Err: err}
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return &Failure{Kind: FailureProtocol, Op: "read daemon API response", Err: err}
	}
	if len(content) > maxResponseSize {
		return &Failure{Kind: FailureProtocol, Op: "read daemon API response", Err: fmt.Errorf("response exceeds %d bytes", maxResponseSize)}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var problem APIError
		if err := json.Unmarshal(content, &problem); err != nil || problem.SchemaVersion != 1 || problem.Code == "" || problem.Message == "" {
			return &Failure{Kind: FailureProtocol, Op: "decode daemon API error", Err: fmt.Errorf("HTTP %d returned an invalid error envelope", response.StatusCode)}
		}
		problem.HTTPStatus = response.StatusCode
		return &problem
	}
	if responseBody == nil || method == http.MethodHead {
		return nil
	}
	if err := json.Unmarshal(content, responseBody); err != nil {
		return &Failure{Kind: FailureProtocol, Op: "decode daemon API response", Err: err}
	}
	return nil
}

// Download sends one authenticated request for a finite binary resource.
func (session *Session) Download(ctx context.Context, resource string) ([]byte, error) {
	resourceURL, err := session.resourceURL(resource)
	if err != nil {
		return nil, &Failure{Kind: FailureProtocol, Op: "build API request", Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, resourceURL, nil)
	if err != nil {
		return nil, &Failure{Kind: FailureProtocol, Op: "build API request", Err: err}
	}
	request.Header.Set("Authorization", session.endpoint.AuthorizationHeader())
	request.Header.Set("Accept", "application/zip")
	response, err := session.client.http.Do(request)
	if err != nil {
		return nil, &Failure{Kind: FailureUnavailable, Op: "call daemon API", Err: err}
	}
	defer response.Body.Close()
	limit := int64(maxDownloadSize)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limit = maxResponseSize
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, &Failure{Kind: FailureProtocol, Op: "read daemon API response", Err: err}
	}
	if int64(len(content)) > limit {
		return nil, &Failure{Kind: FailureProtocol, Op: "read daemon API response", Err: fmt.Errorf("response exceeds %d bytes", limit)}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var problem APIError
		if err := json.Unmarshal(content, &problem); err != nil || problem.SchemaVersion != 1 || problem.Code == "" || problem.Message == "" {
			return nil, &Failure{Kind: FailureProtocol, Op: "decode daemon API error", Err: fmt.Errorf("HTTP %d returned an invalid error envelope", response.StatusCode)}
		}
		problem.HTTPStatus = response.StatusCode
		return nil, &problem
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/zip") {
		return nil, &Failure{Kind: FailureProtocol, Op: "validate daemon API response", Err: fmt.Errorf("unexpected Content-Type %q", contentType)}
	}
	return content, nil
}

// StreamEvents replays the authenticated SSE event stream after one durable
// position. Returning false from consume closes the stream successfully.
func (session *Session) StreamEvents(ctx context.Context, after uint64, consume func(statestore.Event) bool) error {
	if consume == nil {
		return &Failure{Kind: FailureProtocol, Op: "stream daemon events", Err: errors.New("event consumer is required")}
	}
	resourceURL, err := session.resourceURL("events")
	if err != nil {
		return &Failure{Kind: FailureProtocol, Op: "build event stream request", Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, resourceURL, nil)
	if err != nil {
		return &Failure{Kind: FailureProtocol, Op: "build event stream request", Err: err}
	}
	request.Header.Set("Authorization", session.endpoint.AuthorizationHeader())
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", strconv.FormatUint(after, 10))
	streamClient := *session.client.http
	streamClient.Timeout = 0
	response, err := streamClient.Do(request)
	if err != nil {
		return &Failure{Kind: FailureUnavailable, Op: "open daemon event stream", Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
		var problem APIError
		if json.Unmarshal(content, &problem) == nil && problem.SchemaVersion == 1 && problem.Code != "" {
			problem.HTTPStatus = response.StatusCode
			return &problem
		}
		return &Failure{Kind: FailureProtocol, Op: "open daemon event stream", Err: fmt.Errorf("HTTP %d returned an invalid error envelope", response.StatusCode)}
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return &Failure{Kind: FailureProtocol, Op: "validate daemon event stream", Err: fmt.Errorf("unexpected Content-Type %q", response.Header.Get("Content-Type"))}
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), maxResponseSize)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event statestore.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil || event.GlobalPosition == 0 || event.Kind == "" {
			return &Failure{Kind: FailureProtocol, Op: "decode daemon event", Err: errors.New("invalid event envelope")}
		}
		if !consume(event) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return &Failure{Kind: FailureUnavailable, Op: "read daemon event stream", Err: err}
	}
	return &Failure{Kind: FailureUnavailable, Op: "read daemon event stream", Err: io.ErrUnexpectedEOF}
}

func (session *Session) resourceURL(resource string) (string, error) {
	if strings.HasPrefix(resource, "/") {
		return "", errors.New("resource must be relative")
	}
	reference, err := url.Parse(resource)
	if err != nil {
		return "", err
	}
	if reference.IsAbs() || reference.Host != "" || escapesAPIRoot(reference.Path) {
		return "", errors.New("resource must remain within the negotiated API root")
	}
	base := fmt.Sprintf("%s/api/%s/", session.endpoint.BaseURL(), session.version)
	return base + reference.String(), nil
}

func escapesAPIRoot(resourcePath string) bool {
	for _, segment := range strings.Split(resourcePath, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}
