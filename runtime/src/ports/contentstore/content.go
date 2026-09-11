// Package contentstore defines durable versioned authoring content.
package contentstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var ErrNotFound = errors.New("content not found")
var ErrConflict = errors.New("content revision conflict")
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func ValidateVersion(version string) error {
	if len(version) > 128 || !semanticVersionPattern.MatchString(version) {
		return errors.New("version must be a semantic version such as 1.2.3")
	}
	withoutBuild, _, _ := strings.Cut(version, "+")
	if _, prerelease, present := strings.Cut(withoutBuild, "-"); present {
		for _, part := range strings.Split(prerelease, ".") {
			if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
				return errors.New("numeric prerelease identifiers cannot contain leading zeros")
			}
		}
	}
	return nil
}

type Reference struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

func (r Reference) Validate() error {
	if !identifier.MatchString(r.ID) || ValidateVersion(r.Version) != nil || !digestPattern.MatchString(r.Digest) {
		return errors.New("an exact content id, version and SHA-256 digest are required")
	}
	return nil
}

// Document is the closed wire union of artifact structure and task instructions.
// Validate rejects fields belonging to the other kind at every write boundary.
type Document struct {
	Kind             string    `json:"kind"`
	Content          string    `json:"content,omitempty"`
	RequiredHeadings []string  `json:"requiredHeadings,omitempty"`
	Instructions     string    `json:"instructions,omitempty"`
	Sections         []Section `json:"sections,omitempty"`
}

func (d *Document) UnmarshalJSON(raw []byte) error {
	type wire Document
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for field := range fields {
		switch value.Kind {
		case "template":
			if field != "kind" && field != "content" && field != "requiredHeadings" {
				return errors.New("template cannot contain prompt fields")
			}
		case "prompt":
			if field != "kind" && field != "instructions" && field != "sections" {
				return errors.New("prompt cannot contain template fields")
			}
		}
	}
	document := Document(value)
	if err := document.Validate(); err != nil {
		return err
	}
	*d = document
	return nil
}

type Section struct {
	ID           string    `json:"id"`
	When         Condition `json:"when"`
	Instructions string    `json:"instructions"`
}

type Condition struct {
	Kind  string `json:"kind"`
	Input string `json:"input,omitempty"`
}

func (d Document) Validate() error {
	encoded, _ := json.Marshal(d)
	if len(encoded) > 1<<20 {
		return errors.New("content document exceeds 1 MiB")
	}
	switch d.Kind {
	case "template":
		if strings.TrimSpace(d.Content) == "" || d.Instructions != "" || len(d.Sections) != 0 {
			return errors.New("template requires content and cannot contain prompt instructions")
		}
		seen := map[string]bool{}
		for _, heading := range d.RequiredHeadings {
			if strings.TrimSpace(heading) == "" || seen[heading] {
				return errors.New("required headings must be nonempty and unique")
			}
			seen[heading] = true
		}
	case "prompt":
		if strings.TrimSpace(d.Instructions) == "" || d.Content != "" || len(d.RequiredHeadings) != 0 {
			return errors.New("prompt requires instructions and cannot contain template content")
		}
		seen := map[string]bool{}
		for _, section := range d.Sections {
			if !identifier.MatchString(section.ID) || seen[section.ID] || strings.TrimSpace(section.Instructions) == "" {
				return errors.New("prompt sections require unique identifiers and nonempty instructions")
			}
			seen[section.ID] = true
			switch section.When.Kind {
			case "input_linked", "input_absent":
				if !identifier.MatchString(section.When.Input) {
					return errors.New("input condition requires a valid input name")
				}
			case "always", "revision":
				if section.When.Input != "" {
					return errors.New("always and revision conditions cannot name an input")
				}
			default:
				return fmt.Errorf("unsupported prompt condition %q", section.When.Kind)
			}
		}
	default:
		return fmt.Errorf("unsupported content kind %q", d.Kind)
	}
	return nil
}

type Draft struct {
	Revision uint64   `json:"revision"`
	Document Document `json:"document"`
}

type Version struct {
	Reference Reference `json:"reference"`
	Document  Document  `json:"document"`
	CreatedAt time.Time `json:"createdAt"`
}

type Item struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Kind        string     `json:"kind"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	Draft       Draft      `json:"draft"`
	Versions    []Version  `json:"versions"`
}

// Save is an atomic compare-and-swap. expectedRevision zero means create only.
// Implementations retain every published version and reject modification.
type Store interface {
	ContentItems(context.Context) ([]Item, error)
	ContentItem(context.Context, string) (Item, error)
	SaveContentItem(context.Context, Item, uint64) error
}
