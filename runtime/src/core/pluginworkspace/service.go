// Package pluginworkspace supplies scoped workspace and artifact host services.
// Plugin arguments never select a work item, attempt, owner, or backing path.
package pluginworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/workspace"
)

// Artifacts is the existing daemon ingestion/attachment boundary, not a storage
// backend. Implementations preserve immutable revisions and audit evidence.
type Artifacts interface {
	Ingest(context.Context, artifactops.IngestInput, string) (artifactingest.Result, error)
	Attach(context.Context, artifactops.AttachInput, string) (artifactbinding.Version, error)
}

// Scope is created from authorized durable attempt records by daemon composition.
type Scope struct {
	WorkItemID, RunID, NodeID, AttemptID string
	Plugin                               extension.Ref
}

type Service struct {
	manager   workspace.Manager
	artifacts Artifacts
	scope     Scope
}

func New(manager workspace.Manager, artifacts Artifacts, scope Scope) (*Service, error) {
	if manager == nil || artifacts == nil || scope.WorkItemID == "" || scope.RunID == "" || scope.NodeID == "" || scope.AttemptID == "" {
		return nil, errors.New("workspace services require storage, artifacts, and a complete attempt scope")
	}
	if err := scope.Plugin.Validate(); err != nil {
		return nil, err
	}
	return &Service{manager: manager, artifacts: artifacts, scope: scope}, nil
}

// Call implements the plugin host-service callback. Only this family's three
// methods are available; no approval, scheduling, or arbitrary artifact reads.
func (s *Service) Call(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > 1024*1024 {
		return nil, errors.New("workspace request exceeds 1 MiB")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "workspace.read":
		var args struct {
			Area workspace.Area `json:"area"`
			Path string         `json:"path"`
		}
		if err := decode(raw, &args); err != nil {
			return nil, err
		}
		h, err := s.bind(ctx)
		if err != nil {
			return nil, err
		}
		defer h.Close()
		data, err := h.ReadFile(ctx, args.Area, args.Path)
		if err != nil {
			return nil, err
		}
		if len(data) > 512*1024 {
			return nil, errors.New("workspace read exceeds tool response limit")
		}
		if utf8.Valid(data) {
			return json.Marshal(map[string]any{"content": map[string]string{"encoding": "utf8", "text": string(data)}, "digest": digest(data)})
		}
		return json.Marshal(map[string]any{"content": map[string]string{"encoding": "base64", "data": base64.StdEncoding.EncodeToString(data)}, "digest": digest(data)})
	case "workspace.write":
		var args struct {
			Area           workspace.Area  `json:"area"`
			Path           string          `json:"path"`
			Content        json.RawMessage `json:"content"`
			ExpectedDigest *string         `json:"expectedDigest,omitempty"`
		}
		if err := decode(raw, &args); err != nil {
			return nil, err
		}
		data, err := decodeContent(args.Content)
		if err != nil {
			return nil, err
		}
		h, err := s.bind(ctx)
		if err != nil {
			return nil, err
		}
		defer h.Close()
		// A caller must explicitly name the version it replaces. Identical
		// content retries are safe; a stale write cannot overwrite newer bytes.
		if args.ExpectedDigest == nil {
			err = h.WriteFile(ctx, args.Area, args.Path, data)
		} else {
			if len(*args.ExpectedDigest) != 64 || strings.IndexFunc(*args.ExpectedDigest, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) != -1 {
				return nil, errors.New("expectedDigest must be a lowercase SHA-256 digest")
			}
			err = h.ReplaceFile(ctx, args.Area, args.Path, *args.ExpectedDigest, data)
		}
		if err != nil {
			previous, readErr := h.ReadFile(ctx, args.Area, args.Path)
			if readErr != nil || !bytes.Equal(previous, data) {
				return nil, err
			}
		}
		return json.Marshal(map[string]any{"status": "stored", "size": len(data), "digest": digest(data)})
	case "workspace.publish_artifact":
		var args struct {
			Path      string `json:"path"`
			MediaType string `json:"mediaType"`
			Key       string `json:"key"`
		}
		if err := decode(raw, &args); err != nil {
			return nil, err
		}
		if strings.TrimSpace(args.Key) == "" || len(args.Key) > 256 || strings.TrimSpace(args.MediaType) == "" {
			return nil, errors.New("artifact publication requires a media type and stable key of at most 256 bytes")
		}
		h, err := s.bind(ctx)
		if err != nil {
			return nil, err
		}
		defer h.Close()
		data, err := h.ReadFile(ctx, workspace.Staged, args.Path)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("plugin-artifact:%x", sha256.Sum256([]byte(s.scope.WorkItemID+"\x00"+s.scope.RunID+"\x00"+s.scope.AttemptID+"\x00"+s.scope.Plugin.ID+"\x00"+args.Key)))
		result, err := s.artifacts.Ingest(ctx, artifactops.IngestInput{
			GeneratedBy: &artifactregistry.AttemptProvenance{RunID: s.scope.RunID, NodeID: s.scope.NodeID, AttemptID: s.scope.AttemptID},
			SourceKind:  artifactregistry.SourceGenerated, SourceName: path.Base(strings.ReplaceAll(args.Path, "\\", "/")),
			MediaType: args.MediaType, Content: data, Creator: s.scope.Plugin.ID + "@" + s.scope.Plugin.Version + "#" + s.scope.Plugin.Digest,
			Roles: []string{"plugin-output"},
		}, key)
		if err != nil {
			return nil, err
		}
		ref := artifactregistry.VersionRef{ArtifactID: result.Artifact.ArtifactID, Version: result.Artifact.Version}
		if _, err := s.artifacts.Attach(ctx, artifactops.AttachInput{Artifact: ref, Target: artifactbinding.Target{Kind: artifactbinding.TargetWork, ID: s.scope.WorkItemID}}, key+":attach"); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"artifact": ref, "digest": result.Artifact.BlobDigest, "mediaType": result.Artifact.DetectedMediaType})
	default:
		return nil, fmt.Errorf("workspace capability %q is not granted", method)
	}
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (s *Service) bind(ctx context.Context) (workspace.Handle, error) {
	return s.manager.Bind(ctx, workspace.Grant{WorkItemID: s.scope.WorkItemID, PluginID: s.scope.Plugin.ID, AttemptID: s.scope.AttemptID})
}

func decode(raw json.RawMessage, out any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("tool arguments must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func decodeContent(raw json.RawMessage) ([]byte, error) {
	var tag struct {
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(raw, &tag); err != nil {
		return nil, err
	}
	switch tag.Encoding {
	case "utf8":
		var v struct {
			Encoding string  `json:"encoding"`
			Text     *string `json:"text"`
		}
		if err := decode(raw, &v); err != nil {
			return nil, err
		}
		if v.Text == nil {
			return nil, errors.New("UTF-8 content requires text")
		}
		return []byte(*v.Text), nil
	case "base64":
		var v struct {
			Encoding string  `json:"encoding"`
			Data     *string `json:"data"`
		}
		if err := decode(raw, &v); err != nil {
			return nil, err
		}
		if v.Data == nil {
			return nil, errors.New("base64 content requires data")
		}
		return base64.StdEncoding.Strict().DecodeString(*v.Data)
	default:
		return nil, errors.New("content encoding must be utf8 or base64")
	}
}
