// Package contentlibrary owns reusable template and prompt authoring lifecycles.
package contentlibrary

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"darkstar/src/ports/contentstore"
)

type Reference = contentstore.Reference
type Document = contentstore.Document
type Section = contentstore.Section
type Condition = contentstore.Condition
type Draft = contentstore.Draft
type Version = contentstore.Version
type Item = contentstore.Item

var ErrNotFound = contentstore.ErrNotFound
var ErrConflict = contentstore.ErrConflict

type Service struct {
	store contentstore.Store
}

func New(store contentstore.Store) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context) ([]Item, error) {
	return s.store.ContentItems(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (Item, error) {
	return s.store.ContentItem(ctx, id)
}

type CreateRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Document    Document `json:"document"`
}

type UpdateRequest struct {
	ExpectedRevision uint64   `json:"expectedRevision"`
	Document         Document `json:"document"`
	Name             *string  `json:"name,omitempty"`
	Description      *string  `json:"description,omitempty"`
}

type PublishRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	Version          string `json:"version"`
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (Item, error) {
	if strings.TrimSpace(request.Name) == "" {
		return Item{}, errors.New("content name is required")
	}
	if err := request.Document.Validate(); err != nil {
		return Item{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Item{}, err
	}
	item := Item{ID: hex.EncodeToString(random[:]), Name: request.Name, Description: request.Description, Kind: request.Document.Kind, Draft: Draft{Revision: 1, Document: request.Document}, Versions: []Version{}}
	err := s.store.SaveContentItem(ctx, item, 0)
	return item, err
}

func (s *Service) Update(ctx context.Context, id string, request UpdateRequest) (Item, error) {
	item, err := s.editable(ctx, id, request.ExpectedRevision)
	if err != nil {
		return Item{}, err
	}
	if err := request.Document.Validate(); err != nil {
		return Item{}, err
	}
	if request.Document.Kind != item.Kind {
		return Item{}, errors.New("content kind cannot change")
	}
	if request.Name != nil {
		if strings.TrimSpace(*request.Name) == "" {
			return Item{}, errors.New("content name is required")
		}
		item.Name = *request.Name
	}
	if request.Description != nil {
		item.Description = *request.Description
	}
	item.Draft = Draft{Revision: request.ExpectedRevision + 1, Document: request.Document}
	err = s.store.SaveContentItem(ctx, item, request.ExpectedRevision)
	return item, err
}

// Publish is an operator authoring action. Execution tools receive Resolve only.
func (s *Service) Publish(ctx context.Context, id string, request PublishRequest) (Item, error) {
	item, err := s.editable(ctx, id, request.ExpectedRevision)
	if err != nil {
		return Item{}, err
	}
	if err := contentstore.ValidateVersion(request.Version); err != nil {
		return Item{}, errors.New("version must be a semantic version such as 1.2.3")
	}
	for _, version := range item.Versions {
		if version.Reference.Version == request.Version {
			return Item{}, fmt.Errorf("%w: published version already exists", ErrConflict)
		}
	}
	if err := item.Draft.Document.Validate(); err != nil {
		return Item{}, err
	}
	item.Versions = append(item.Versions, Version{Reference: Reference{ID: id, Version: request.Version, Digest: Digest(item.Draft.Document)}, Document: item.Draft.Document, CreatedAt: time.Now().UTC()})
	item.Draft.Revision++
	err = s.store.SaveContentItem(ctx, item, request.ExpectedRevision)
	return item, err
}

func Digest(document Document) string {
	encoded, _ := json.Marshal(document)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (s *Service) Resolve(ctx context.Context, reference Reference) (Version, error) {
	if err := reference.Validate(); err != nil {
		return Version{}, err
	}
	item, err := s.Get(ctx, reference.ID)
	if err != nil {
		return Version{}, err
	}
	for _, version := range item.Versions {
		if version.Reference.Version == reference.Version {
			if version.Reference != reference || Digest(version.Document) != reference.Digest {
				return Version{}, fmt.Errorf("%w: published content digest mismatch", ErrConflict)
			}
			return version, nil
		}
	}
	return Version{}, ErrNotFound
}

func (s *Service) Duplicate(ctx context.Context, id, name string) (Item, error) {
	item, err := s.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	return s.Create(ctx, CreateRequest{Name: name, Description: item.Description, Document: item.Draft.Document})
}

func (s *Service) Archive(ctx context.Context, id string, archived bool) (Item, error) {
	item, err := s.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	revision := item.Draft.Revision
	item.ArchivedAt = nil
	if archived {
		now := time.Now().UTC()
		item.ArchivedAt = &now
	}
	item.Draft.Revision++
	err = s.store.SaveContentItem(ctx, item, revision)
	return item, err
}

func (s *Service) editable(ctx context.Context, id string, revision uint64) (Item, error) {
	item, err := s.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	if item.ArchivedAt != nil || revision == 0 || revision != item.Draft.Revision {
		return Item{}, fmt.Errorf("%w: reload the current draft or restore the archived item", ErrConflict)
	}
	return item, nil
}

// Seed installs shipped definitions only when their stable identity is absent.
// Existing drafts, publications and archive choices are never overwritten.
func (s *Service) Seed(ctx context.Context, items []Item) error {
	for _, item := range items {
		if _, err := s.Get(ctx, item.ID); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := s.store.SaveContentItem(ctx, item, 0); err != nil && !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return nil
}
