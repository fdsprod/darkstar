// Package api implements DARKSTAR's authenticated, versioned loopback HTTP boundary.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	// EndpointSchemaVersion is the durable endpoint discovery format.
	EndpointSchemaVersion = 1
	endpointFileName      = "endpoint.json"
	tokenBytes            = 32
)

// Version names one compatible HTTP representation family.
type Version string

const (
	// VersionV1 is the MVP API rooted at /api/v1.
	VersionV1 Version = "v1"
)

// ErrNoCompatibleVersion means the client and daemon share no API version.
var ErrNoCompatibleVersion = errors.New("no compatible API version")

// Token keeps the credential opaque in ordinary formatting while still
// supporting endpoint-file JSON and Authorization header construction.
type Token struct{ value [tokenBytes]byte }

func newToken() (Token, error) {
	var token Token
	if _, err := rand.Read(token.value[:]); err != nil {
		return Token{}, fmt.Errorf("generate API token: %w", err)
	}
	return token, nil
}

func parseToken(value string) (Token, error) {
	if len(value) != tokenBytes*2 {
		return Token{}, errors.New("token must be 256-bit lowercase hexadecimal")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return Token{}, errors.New("token must be 256-bit lowercase hexadecimal")
	}
	if value != hex.EncodeToString(decoded) {
		return Token{}, errors.New("token must be 256-bit lowercase hexadecimal")
	}
	var token Token
	copy(token.value[:], decoded)
	return token, nil
}

func (t Token) encoded() string { return hex.EncodeToString(t.value[:]) }

func (t Token) matches(value string) bool {
	candidate, err := hex.DecodeString(value)
	if err != nil || len(candidate) != tokenBytes {
		return false
	}
	return subtle.ConstantTimeCompare(t.value[:], candidate) == 1
}

func (t Token) equal(other Token) bool {
	return subtle.ConstantTimeCompare(t.value[:], other.value[:]) == 1
}

// String prevents accidental credential disclosure through logs and errors.
func (Token) String() string { return "[redacted]" }

// GoString prevents %#v from revealing the token's backing bytes.
func (Token) GoString() string { return "api.Token([redacted])" }

func (t Token) MarshalJSON() ([]byte, error) { return json.Marshal(t.encoded()) }

func (t *Token) UnmarshalJSON(content []byte) error {
	var value string
	if err := json.Unmarshal(content, &value); err != nil {
		return errors.New("token must be a hexadecimal string")
	}
	parsed, err := parseToken(value)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// Endpoint is the single discovery snapshot atomically replaced whenever the
// daemon starts or its credential rotates.
type Endpoint struct {
	SchemaVersion    int       `json:"schemaVersion"`
	APIVersion       Version   `json:"apiVersion"`
	PID              int       `json:"pid"`
	ProcessStartedAt time.Time `json:"processStartedAt"`
	Port             int       `json:"port"`
	Token            Token     `json:"token"`
	CreatedAt        time.Time `json:"createdAt"`
}

// BaseURL returns the only address family the MVP server publishes.
func (e Endpoint) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", e.Port) }

// AuthorizationHeader returns the complete bearer credential for an HTTP client.
func (e Endpoint) AuthorizationHeader() string { return "Bearer " + e.Token.encoded() }

// SupportedVersions returns a copy of the server's compatibility preference.
func SupportedVersions() []Version { return []Version{VersionV1} }

// NegotiateVersion selects the endpoint version only when the client explicitly
// supports it. A future multi-version daemon can extend the endpoint contract.
func NegotiateVersion(endpoint Endpoint, clientVersions []Version) (Version, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return "", err
	}
	for _, candidate := range clientVersions {
		if candidate == endpoint.APIVersion {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: daemon=%s client=%v", ErrNoCompatibleVersion, endpoint.APIVersion, clientVersions)
}

// ReadEndpoint reads and strictly validates the protected daemon discovery file.
func ReadEndpoint(runtimeDirectory string) (Endpoint, error) {
	path, err := endpointPath(runtimeDirectory)
	if err != nil {
		return Endpoint{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Endpoint{}, fmt.Errorf("open API endpoint: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	var endpoint Endpoint
	if err := decoder.Decode(&endpoint); err != nil {
		return Endpoint{}, fmt.Errorf("decode API endpoint: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Endpoint{}, errors.New("decode API endpoint: multiple JSON values")
		}
		return Endpoint{}, fmt.Errorf("decode API endpoint: %w", err)
	}
	if err := validateEndpoint(endpoint); err != nil {
		return Endpoint{}, fmt.Errorf("validate API endpoint: %w", err)
	}
	return endpoint, nil
}

func validateEndpoint(endpoint Endpoint) error {
	if endpoint.SchemaVersion != EndpointSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d", endpoint.SchemaVersion)
	}
	if endpoint.APIVersion != VersionV1 {
		return fmt.Errorf("unsupported apiVersion %q", endpoint.APIVersion)
	}
	if endpoint.PID <= 0 {
		return errors.New("pid must be positive")
	}
	if endpoint.ProcessStartedAt.IsZero() {
		return errors.New("processStartedAt is required")
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", endpoint.Port)
	}
	if endpoint.Token == (Token{}) {
		return errors.New("token is required")
	}
	if endpoint.CreatedAt.IsZero() {
		return errors.New("createdAt is required")
	}
	return nil
}

func endpointPath(runtimeDirectory string) (string, error) {
	if !filepath.IsAbs(runtimeDirectory) {
		return "", fmt.Errorf("API runtime directory must be absolute: %q", runtimeDirectory)
	}
	return filepath.Join(filepath.Clean(runtimeDirectory), endpointFileName), nil
}

func writeEndpoint(path string, endpoint Endpoint) error {
	if err := validateEndpoint(endpoint); err != nil {
		return fmt.Errorf("validate API endpoint: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create API runtime directory: %w", err)
	}
	content, err := json.MarshalIndent(endpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("encode API endpoint: %w", err)
	}
	content = append(content, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".endpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary API endpoint: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary API endpoint: %w", err)
	}
	if err := protectFile(temporaryPath); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary API endpoint: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary API endpoint: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush temporary API endpoint: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary API endpoint: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish API endpoint: %w", err)
	}
	if err := verifyProtectedFile(path); err != nil {
		return fmt.Errorf("verify API endpoint protection: %w", err)
	}
	return nil
}

func removeEndpointIfOwned(path string, owned Endpoint) error {
	current, err := ReadEndpoint(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return nil // Preserve an unknown file rather than delete another instance's state.
	}
	if current.Port != owned.Port || current.CreatedAt != owned.CreatedAt || !current.Token.equal(owned.Token) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove API endpoint: %w", err)
	}
	return nil
}
