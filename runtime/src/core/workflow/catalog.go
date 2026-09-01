package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/config"
	"github.com/fdsprod/darkstar/runtime/src/ports/workflowstore"
)

// LoadedDefinition is one validated canonical workflow selected from configured scopes.
type LoadedDefinition struct {
	Document      Document
	CanonicalJSON json.RawMessage
	Digest        string
	SourceScope   workflowstore.Scope
	SourceRef     string
}

// InstallResult reports whether installation created a new immutable version.
type InstallResult struct {
	Version workflowstore.InstalledVersion
	Created bool
}

// Catalog coordinates scope-aware loading, version installation, and run snapshots.
type Catalog struct {
	source workflowstore.Source
	store  workflowstore.Store
	now    func() time.Time
}

func NewCatalog(source workflowstore.Source, store workflowstore.Store) (*Catalog, error) {
	if source == nil || store == nil {
		return nil, errors.New("workflow catalog requires a source and store")
	}
	return &Catalog{source: source, store: store, now: time.Now}, nil
}

// Canonicalize validates an authored JSON workflow and returns its canonical
// representation and lowercase SHA-256 identity.
func Canonicalize(content []byte) (Document, json.RawMessage, string, error) {
	if err := rejectDuplicateJSONKeys(content); err != nil {
		return Document{}, nil, "", fmt.Errorf("decode workflow: %w", err)
	}
	document, err := Decode(content)
	if err != nil {
		return Document{}, nil, "", err
	}
	typedJSON, err := json.Marshal(document)
	if err != nil {
		return Document{}, nil, "", fmt.Errorf("canonicalize workflow: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(typedJSON))
	decoder.UseNumber()
	var canonicalValue any
	if err := decoder.Decode(&canonicalValue); err != nil {
		return Document{}, nil, "", fmt.Errorf("canonicalize workflow: %w", err)
	}
	canonical, err := json.Marshal(canonicalValue)
	if err != nil {
		return Document{}, nil, "", fmt.Errorf("canonicalize workflow: %w", err)
	}
	if _, err := Decode(canonical); err != nil {
		return Document{}, nil, "", fmt.Errorf("validate canonical workflow: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return document, canonical, hex.EncodeToString(digest[:]), nil
}

func rejectDuplicateJSONKeys(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if _, exists := keys[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				keys[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(map[json.Delim]rune{'{': '}', '[': ']'}[delimiter]) {
			return errors.New("mismatched JSON delimiter")
		}
		return nil
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow contains more than one JSON value")
		}
		return err
	}
	return nil
}

// Load selects the highest-precedence source for every workflow name/version.
// Duplicate definitions within one scope fail instead of depending on filename order.
func (c *Catalog) Load(ctx context.Context) ([]LoadedDefinition, error) {
	candidates, err := c.source.Load(ctx)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]LoadedDefinition, len(candidates))
	for _, candidate := range candidates {
		loaded, err := loadCandidate(candidate)
		if err != nil {
			return nil, err
		}
		key := loaded.Document.Metadata.Name + "\x00" + loaded.Document.Metadata.Version
		if current, exists := selected[key]; exists {
			if current.SourceScope == loaded.SourceScope {
				return nil, fmt.Errorf("workflow %s %s is defined more than once in %s scope (%q and %q)",
					loaded.Document.Metadata.Name, loaded.Document.Metadata.Version, loaded.SourceScope, current.SourceRef, loaded.SourceRef)
			}
			if scopePrecedence(current.SourceScope) > scopePrecedence(loaded.SourceScope) {
				continue
			}
		}
		selected[key] = loaded
	}
	loaded := make([]LoadedDefinition, 0, len(selected))
	for _, definition := range selected {
		loaded = append(loaded, definition)
	}
	sort.Slice(loaded, func(i, j int) bool {
		if loaded[i].Document.Metadata.Name != loaded[j].Document.Metadata.Name {
			return loaded[i].Document.Metadata.Name < loaded[j].Document.Metadata.Name
		}
		return loaded[i].Document.Metadata.Version < loaded[j].Document.Metadata.Version
	})
	return loaded, nil
}

// Install validates and installs one explicitly selected authored candidate.
func (c *Catalog) Install(ctx context.Context, candidate workflowstore.Candidate) (InstallResult, error) {
	definition, err := loadCandidate(candidate)
	if err != nil {
		return InstallResult{}, err
	}
	return c.install(ctx, definition)
}

// InstallConfigured validates and installs every selected configured version.
func (c *Catalog) InstallConfigured(ctx context.Context) ([]InstallResult, error) {
	definitions, err := c.Load(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]InstallResult, 0, len(definitions))
	for _, definition := range definitions {
		result, err := c.install(ctx, definition)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func loadCandidate(candidate workflowstore.Candidate) (LoadedDefinition, error) {
	if !validSourceScope(candidate.Scope) || candidate.Reference == "" {
		return LoadedDefinition{}, fmt.Errorf("workflow candidate has invalid source %q at %q", candidate.Scope, candidate.Reference)
	}
	document, canonical, digest, err := Canonicalize(candidate.Content)
	if err != nil {
		return LoadedDefinition{}, fmt.Errorf("workflow %s %q: %w", candidate.Scope, candidate.Reference, err)
	}
	return LoadedDefinition{
		Document: document, CanonicalJSON: canonical, Digest: digest,
		SourceScope: candidate.Scope, SourceRef: candidate.Reference,
	}, nil
}

func (c *Catalog) install(ctx context.Context, definition LoadedDefinition) (InstallResult, error) {
	version, created, err := c.store.Install(ctx, workflowstore.InstallRequest{
		Name: definition.Document.Metadata.Name, Version: definition.Document.Metadata.Version,
		Digest: definition.Digest, Document: definition.CanonicalJSON,
		SourceScope: definition.SourceScope, SourceRef: definition.SourceRef,
		InstalledAt: c.now().UTC().Round(0),
	})
	if err != nil {
		return InstallResult{}, fmt.Errorf("install workflow %s %s: %w", definition.Document.Metadata.Name, definition.Document.Metadata.Version, err)
	}
	return InstallResult{Version: version, Created: created}, nil
}

// SnapshotRun freezes the installed workflow and complete effective
// configuration, including source attribution, for an existing run.
func (c *Catalog) SnapshotRun(ctx context.Context, runID, name, version string, effective config.Effective) (workflowstore.RunSnapshot, bool, error) {
	installed, err := c.store.InstalledVersion(ctx, name, version)
	if err != nil {
		return workflowstore.RunSnapshot{}, false, err
	}
	configSnapshot, err := json.Marshal(struct {
		Values  map[string]any           `json:"values"`
		Sources map[string]config.Source `json:"sources"`
	}{Values: effective.Values(), Sources: effective.Sources()})
	if err != nil {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("snapshot effective configuration: %w", err)
	}
	configHash := sha256.Sum256(configSnapshot)
	return c.store.CreateRunSnapshot(ctx, workflowstore.RunSnapshotRequest{
		RunID: runID, WorkflowName: installed.Name, WorkflowVersion: installed.Version,
		WorkflowDigest: installed.Digest, WorkflowDocument: installed.Document,
		ConfigDigest: hex.EncodeToString(configHash[:]), ConfigSnapshot: configSnapshot,
		CreatedAt: c.now().UTC().Round(0),
	})
}

func validSourceScope(scope workflowstore.Scope) bool { return scopePrecedence(scope) != 0 }

func scopePrecedence(scope workflowstore.Scope) int {
	switch scope {
	case workflowstore.ScopeDefault:
		return 1
	case workflowstore.ScopeUser:
		return 2
	case workflowstore.ScopeProject:
		return 3
	default:
		return 0
	}
}
