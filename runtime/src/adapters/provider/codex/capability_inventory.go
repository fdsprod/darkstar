package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	registryport "darkstar/src/ports/capabilityregistry"
)

const (
	maxObservedSkillFiles = 512
	maxObservedSkillBytes = 32 << 20
)

var _ registryport.Observer = (*Adapter)(nil)

type skillsListResponse struct {
	Data []struct {
		CWD    string `json:"cwd"`
		Errors []struct {
			Message string `json:"message"`
			Path    string `json:"path"`
		} `json:"errors"`
		Skills []codexSkillMetadata `json:"skills"`
	} `json:"data"`
}

type codexSkillMetadata struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Enabled          bool            `json:"enabled"`
	Path             string          `json:"path"`
	Scope            string          `json:"scope"`
	PluginID         *string         `json:"pluginId"`
	ShortDescription *string         `json:"shortDescription"`
	Interface        json.RawMessage `json:"interface"`
	Dependencies     json.RawMessage `json:"dependencies"`
}

type mcpStatusResponse struct {
	Data       []codexMCPServer `json:"data"`
	NextCursor *string          `json:"nextCursor"`
}

type codexMCPServer struct {
	Name          string                  `json:"name"`
	AuthStatus    string                  `json:"authStatus"`
	RuntimeStatus *string                 `json:"runtimeStatus"`
	PluginID      *string                 `json:"pluginId"`
	ServerInfo    json.RawMessage         `json:"serverInfo"`
	Tools         map[string]codexMCPTool `json:"tools"`
}

type codexMCPTool struct {
	Name         string          `json:"name"`
	Title        *string         `json:"title"`
	Description  *string         `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  json.RawMessage `json:"annotations"`
	Meta         json.RawMessage `json:"_meta"`
}

// ObserveCapabilities reads only the admitted App Server's inventory. It does
// not scan PATH, execute skills, start OAuth, or mutate Codex configuration.
func (adapter *Adapter) ObserveCapabilities(ctx context.Context) (registryport.ObservationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return registryport.ObservationSnapshot{}, err
	}
	client, _, err := adapter.factory(ctx)
	if err != nil {
		return registryport.ObservationSnapshot{}, fmt.Errorf("start Codex capability inventory: %w", err)
	}
	observed, observeErr := adapter.observeCapabilities(ctx, client)
	closeErr := client.Close()
	if observeErr != nil {
		return registryport.ObservationSnapshot{}, observeErr
	}
	if closeErr != nil {
		return registryport.ObservationSnapshot{}, fmt.Errorf("close Codex capability inventory: %w", closeErr)
	}
	return observed, nil
}

func (adapter *Adapter) observeCapabilities(ctx context.Context, client *AppServerClient) (registryport.ObservationSnapshot, error) {
	version := client.ProviderVersion()
	if version == "" {
		return registryport.ObservationSnapshot{}, errors.New("codex capability inventory requires an initialized client")
	}
	observedAt := adapter.clock().UTC()
	capabilities, err := adapter.observeSkills(ctx, client, version, observedAt)
	if err != nil {
		return registryport.ObservationSnapshot{}, err
	}
	tools, err := adapter.observeMCPTools(ctx, client, version, observedAt)
	if err != nil {
		return registryport.ObservationSnapshot{}, err
	}
	capabilities = append(capabilities, tools...)
	sort.Slice(capabilities, func(i, j int) bool {
		if capabilities[i].Scope != capabilities[j].Scope {
			return capabilities[i].Scope < capabilities[j].Scope
		}
		if capabilities[i].Name != capabilities[j].Name {
			return capabilities[i].Name < capabilities[j].Name
		}
		return capabilities[i].Kind < capabilities[j].Kind
	})
	hostDigest := sha256.Sum256([]byte("codex-app-server\x00" + version + "\x00" + filepath.Clean(adapter.projectRoot)))
	return registryport.ObservationSnapshot{
		Provider: providerName, HostFingerprint: hex.EncodeToString(hostDigest[:]), Capabilities: capabilities,
	}, nil
}

func (adapter *Adapter) observeSkills(ctx context.Context, client *AppServerClient, version string, observedAt time.Time) ([]registryport.Observation, error) {
	params := struct {
		CWDs        []string `json:"cwds"`
		ForceReload bool     `json:"forceReload"`
	}{CWDs: []string{adapter.projectRoot}, ForceReload: true}
	var response skillsListResponse
	if err := client.Call(ctx, "skills/list", params, &response); err != nil {
		return nil, fmt.Errorf("read Codex skill inventory: %w", err)
	}
	capabilities := make([]registryport.Observation, 0)
	for _, entry := range response.Data {
		if !samePath(entry.CWD, adapter.projectRoot) {
			return nil, fmt.Errorf("codex skill inventory returned unexpected workspace %q", entry.CWD)
		}
		if len(entry.Errors) != 0 {
			return nil, fmt.Errorf("codex skill inventory for %q reported %d discovery errors", entry.CWD, len(entry.Errors))
		}
		for _, skill := range entry.Skills {
			scope, name, err := skillCapabilityIdentity(skill.Scope, skill.Name)
			if err != nil {
				return nil, err
			}
			fingerprint, err := fingerprintSkillPackage(version, skill)
			if err != nil {
				return nil, err
			}
			availability := registryport.AvailabilityAvailable
			if !skill.Enabled {
				availability = registryport.AvailabilityUnavailable
			}
			capabilities = append(capabilities, registryport.Observation{
				Name: name, Kind: registryport.KindSkill, Scope: scope, Fingerprint: fingerprint,
				Source:       registryport.Source{Type: "codex_skill", Locator: filepath.Clean(skill.Path)},
				Dependencies: []string{}, Availability: availability, ObservedAt: observedAt,
			})
		}
	}
	return capabilities, nil
}

func (adapter *Adapter) observeMCPTools(ctx context.Context, client *AppServerClient, version string, observedAt time.Time) ([]registryport.Observation, error) {
	capabilities := make([]registryport.Observation, 0)
	seenCursors := map[string]struct{}{}
	var cursor *string
	for {
		params := struct {
			Cursor   *string `json:"cursor,omitempty"`
			Detail   string  `json:"detail"`
			ThreadID *string `json:"threadId,omitempty"`
		}{Cursor: cursor, Detail: "toolsAndAuthOnly"}
		var response mcpStatusResponse
		if err := client.Call(ctx, "mcpServerStatus/list", params, &response); err != nil {
			return nil, fmt.Errorf("read Codex MCP inventory: %w", err)
		}
		for _, server := range response.Data {
			serverName, err := canonicalInventoryPath(server.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid Codex MCP server name %q: %w", server.Name, err)
			}
			availability := mcpAvailability(server.AuthStatus, server.RuntimeStatus)
			toolKeys := make([]string, 0, len(server.Tools))
			for key := range server.Tools {
				toolKeys = append(toolKeys, key)
			}
			sort.Strings(toolKeys)
			for _, key := range toolKeys {
				tool := server.Tools[key]
				toolName, err := canonicalInventoryPath(tool.Name)
				if err != nil {
					return nil, fmt.Errorf("invalid Codex MCP tool name %q: %w", tool.Name, err)
				}
				fingerprint, err := fingerprintInventory(version, "mcp", struct {
					Server codexMCPServer `json:"server"`
					Tool   codexMCPTool   `json:"tool"`
				}{Server: serverFingerprintMetadata(server), Tool: tool})
				if err != nil {
					return nil, err
				}
				capabilities = append(capabilities, registryport.Observation{
					Name: serverName + "/" + toolName, Kind: registryport.KindTool, Scope: registryport.ObservationCodex,
					Fingerprint: fingerprint, Source: registryport.Source{Type: "codex_mcp_tool", Locator: server.Name + "/" + tool.Name},
					Dependencies: []string{}, Availability: availability, ObservedAt: observedAt,
				})
			}
		}
		if response.NextCursor == nil || strings.TrimSpace(*response.NextCursor) == "" {
			break
		}
		next := strings.TrimSpace(*response.NextCursor)
		if _, duplicate := seenCursors[next]; duplicate {
			return nil, errors.New("codex MCP inventory repeated a pagination cursor")
		}
		seenCursors[next] = struct{}{}
		cursor = &next
	}
	return capabilities, nil
}

func skillCapabilityIdentity(scope, name string) (registryport.ObservationScope, string, error) {
	canonicalName, err := canonicalInventoryPath(name)
	if err != nil {
		return "", "", fmt.Errorf("invalid Codex skill name %q: %w", name, err)
	}
	switch scope {
	case "repo":
		return registryport.ObservationProject, canonicalName, nil
	case "user":
		return registryport.ObservationUser, canonicalName, nil
	case "system", "admin":
		return registryport.ObservationCodex, scope + "/" + canonicalName, nil
	default:
		return "", "", fmt.Errorf("unsupported Codex skill scope %q", scope)
	}
}

func canonicalInventoryPath(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", errors.New("name is empty")
	}
	var result strings.Builder
	separator := false
	for _, character := range value {
		allowed := (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' || character == '/'
		if allowed {
			result.WriteRune(character)
			separator = false
			continue
		}
		if !separator {
			result.WriteByte('-')
			separator = true
		}
	}
	normalized := strings.Trim(result.String(), "-._/")
	if normalized == "" || !isASCIIAlphaNumeric(normalized[0]) {
		return "", errors.New("name has no canonical leading character")
	}
	return normalized, nil
}

func isASCIIAlphaNumeric(character byte) bool {
	return (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')
}

func mcpAvailability(auth string, runtime *string) registryport.Availability {
	if auth == "notLoggedIn" {
		return registryport.AvailabilityUnavailable
	}
	if runtime == nil {
		return registryport.AvailabilityUnavailable
	}
	switch *runtime {
	case "connected":
		return registryport.AvailabilityAvailable
	case "failed", "cancelled":
		return registryport.AvailabilityUnhealthy
	default:
		return registryport.AvailabilityUnavailable
	}
}

func serverFingerprintMetadata(server codexMCPServer) codexMCPServer {
	return codexMCPServer{Name: server.Name, PluginID: server.PluginID, ServerInfo: server.ServerInfo}
}

func fingerprintInventory(version, kind string, value any) (string, error) {
	payload, err := json.Marshal(struct {
		ProviderVersion string `json:"providerVersion"`
		Kind            string `json:"kind"`
		Value           any    `json:"value"`
	}{ProviderVersion: version, Kind: kind, Value: value})
	if err != nil {
		return "", fmt.Errorf("fingerprint Codex %s inventory: %w", kind, err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func fingerprintSkillPackage(version string, skill codexSkillMetadata) (string, error) {
	skillPath := filepath.Clean(skill.Path)
	if !strings.EqualFold(filepath.Base(skillPath), "SKILL.md") {
		return "", fmt.Errorf("codex skill %q locator does not name SKILL.md", skill.Name)
	}
	type packageFile struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
	}
	files := make([]packageFile, 0)
	totalBytes := int64(0)
	root := filepath.Dir(skillPath)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill package contains symbolic link %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill package contains non-regular file %q", path)
		}
		if len(files) >= maxObservedSkillFiles || totalBytes+info.Size() > maxObservedSkillBytes {
			return errors.New("skill package exceeds capability fingerprint limits")
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		totalBytes += int64(len(content))
		digest := sha256.Sum256(content)
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, packageFile{Path: filepath.ToSlash(relative), Digest: hex.EncodeToString(digest[:])})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint Codex skill %q: %w", skill.Name, err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("fingerprint Codex skill %q: package is empty", skill.Name)
	}
	return fingerprintInventory(version, "skill", struct {
		Name             string          `json:"name"`
		Description      string          `json:"description"`
		Path             string          `json:"path"`
		Scope            string          `json:"scope"`
		PluginID         *string         `json:"pluginId"`
		ShortDescription *string         `json:"shortDescription"`
		Interface        json.RawMessage `json:"interface"`
		Dependencies     json.RawMessage `json:"dependencies"`
		Files            []packageFile   `json:"files"`
	}{
		Name: skill.Name, Description: skill.Description, Path: skillPath, Scope: skill.Scope,
		PluginID: skill.PluginID, ShortDescription: skill.ShortDescription,
		Interface: skill.Interface, Dependencies: skill.Dependencies, Files: files,
	})
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
