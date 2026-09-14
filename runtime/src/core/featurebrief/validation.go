// Package featurebrief validates immutable briefs before investigation consumes
// them. It does not create revisions, approve plans, or publish tracker records.
package featurebrief

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"darkstar/src/ports/valueschema"
)

// These schema copies are checked against the canonical contracts by tests.
//
//go:embed feature-planning-v1alpha1.schema.json
var schema json.RawMessage

//go:embed planning-artifact-v1alpha1.schema.json
var legacySchema json.RawMessage

type Reference struct {
	ArtifactID string `json:"artifactId"`
	Version    uint64 `json:"version"`
	SHA256     string `json:"sha256"`
}

type Loader func(context.Context, Reference) (json.RawMessage, error)

type Validator struct {
	Schema valueschema.Validator
	Load   Loader
}

type entry struct {
	Key          string   `json:"key"`
	EvidenceKeys []string `json:"evidenceKeys"`
	State        struct {
		EvidenceKeys []string `json:"evidenceKeys"`
	} `json:"state"`
}

type brief struct {
	ArtifactType string  `json:"artifactType"`
	ProjectID    string  `json:"projectId"`
	FeatureKey   string  `json:"featureKey"`
	Requirements []entry `json:"requirements"`
	Evidence     []entry `json:"evidence"`
	Decisions    []entry `json:"decisions"`
	Repositories []struct {
		RepositoryID string `json:"repositoryId"`
	} `json:"repositories"`
	Lineage struct {
		Kind        string            `json:"kind"`
		Previous    Reference         `json:"previous"`
		Source      Reference         `json:"source"`
		KeyMappings []json.RawMessage `json:"keyMappings"`
	} `json:"lineage"`
}

// Validate checks the brief's closed schema, internal references and exact
// revision lineage. Referenced source bytes never become extra model inputs.
func (v Validator) Validate(ctx context.Context, projectID string, content json.RawMessage) error {
	if v.Schema == nil || v.Load == nil {
		return errors.New("feature brief validation requires schema and exact artifact loading")
	}
	_, err := v.validate(ctx, projectID, content, map[Reference]bool{}, 0)
	return err
}

func (v Validator) validate(ctx context.Context, projectID string, content json.RawMessage, visited map[Reference]bool, depth int) (brief, error) {
	var value brief
	if err := ctx.Err(); err != nil {
		return value, err
	}
	if depth > 32 || len(content) == 0 || len(content) > 1<<20 {
		return value, errors.New("feature brief exceeds bounded validation limits")
	}
	if err := v.Schema.Validate(schema, content); err != nil {
		return value, fmt.Errorf("invalid feature brief schema: %w", err)
	}
	if err := json.Unmarshal(content, &value); err != nil {
		return value, err
	}
	if value.ArtifactType != "feature_brief" || value.ProjectID != projectID {
		return value, errors.New("feature brief type or project does not match investigation")
	}
	evidence, err := uniqueEntries(value.Evidence)
	if err != nil {
		return value, err
	}
	for _, catalog := range [][]entry{value.Requirements, value.Decisions} {
		if _, err := uniqueEntries(catalog); err != nil {
			return value, err
		}
		for _, item := range catalog {
			for _, key := range append(append([]string{}, item.EvidenceKeys...), item.State.EvidenceKeys...) {
				if !evidence[key] {
					return value, fmt.Errorf("feature brief evidence reference %q is missing", key)
				}
			}
		}
	}
	repositories := map[string]bool{}
	for _, repository := range value.Repositories {
		if repositories[repository.RepositoryID] {
			return value, errors.New("feature brief repository catalog contains duplicate identities")
		}
		repositories[repository.RepositoryID] = true
	}
	switch value.Lineage.Kind {
	case "initial":
		return value, nil
	case "revision":
		source, err := v.load(ctx, value.Lineage.Previous, visited)
		if err != nil {
			return value, err
		}
		previous, err := v.validate(ctx, projectID, source, visited, depth+1)
		if err != nil {
			return value, err
		}
		if previous.FeatureKey != value.FeatureKey {
			return value, errors.New("feature brief revision changes feature identity")
		}
	case "migration":
		source, err := v.load(ctx, value.Lineage.Source, visited)
		if err != nil {
			return value, err
		}
		if err := v.Schema.Validate(legacySchema, source); err != nil {
			return value, fmt.Errorf("invalid feature brief migration source: %w", err)
		}
		var previous struct {
			ArtifactType string `json:"artifactType"`
		}
		if err := json.Unmarshal(source, &previous); err != nil {
			return value, err
		}
		if previous.ArtifactType != "product_brief" || len(value.Lineage.KeyMappings) != 0 {
			return value, errors.New("feature brief migration requires an exact product brief and no story key mappings")
		}
	default:
		return value, errors.New("unsupported feature brief lineage")
	}
	return value, nil
}

func uniqueEntries(values []entry) (map[string]bool, error) {
	keys := map[string]bool{}
	for _, value := range values {
		if keys[value.Key] {
			return nil, fmt.Errorf("duplicate feature brief catalog key %q", value.Key)
		}
		keys[value.Key] = true
	}
	return keys, nil
}

func (v Validator) load(ctx context.Context, reference Reference, visited map[Reference]bool) (json.RawMessage, error) {
	if visited[reference] {
		return nil, errors.New("feature brief lineage contains a cycle")
	}
	visited[reference] = true
	content, err := v.Load(ctx, reference)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > 1<<20 {
		return nil, errors.New("feature brief source exceeds bounded validation limits")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != reference.SHA256 {
		return nil, errors.New("feature brief source digest does not match exact reference")
	}
	return content, nil
}
