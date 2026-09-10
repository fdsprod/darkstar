// Package extensions implements immutable, family-typed extension catalogs.
// Construction belongs to daemon composition; there are no global registrations.
package extensions

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"darkstar/src/ports/extension"
	"darkstar/src/ports/valueschema"
)

type Registration[T any] struct {
	Descriptor     extension.Descriptor
	Implementation T
}

type Catalog[T any] struct {
	entries map[extension.Ref]Registration[T]
}

func New[T any](entries ...Registration[T]) (*Catalog[T], error) {
	return NewWithCapabilities[T](nil, entries...)
}

func NewWithCapabilities[T any](granted []string, entries ...Registration[T]) (*Catalog[T], error) {
	allowed := map[string]bool{}
	for _, capability := range granted {
		allowed[capability] = true
	}
	c := &Catalog[T]{entries: make(map[extension.Ref]Registration[T], len(entries))}
	identities := map[string]bool{}
	for _, entry := range entries {
		if err := entry.Descriptor.Validate(); err != nil {
			return nil, err
		}
		v := reflect.ValueOf(entry.Implementation)
		if !v.IsValid() {
			return nil, fmt.Errorf("extension implementation is required")
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice:
			if v.IsNil() {
				return nil, fmt.Errorf("extension implementation is required")
			}
		}
		for _, capability := range entry.Descriptor.RequiredCapabilities {
			if !allowed[capability] {
				return nil, fmt.Errorf("EXTENSION_CAPABILITY_DENIED: %s requires %s", entry.Descriptor.Ref.ID, capability)
			}
		}
		ref := entry.Descriptor.Ref
		key := ref.ID + "@" + ref.Version
		if identities[key] {
			return nil, fmt.Errorf("EXTENSION_CONFLICT: duplicate %s", key)
		}
		identities[key] = true
		entry.Descriptor = clone(entry.Descriptor)
		c.entries[ref] = entry
	}
	return c, nil
}

func (c *Catalog[T]) Resolve(ref extension.Ref) (T, error) {
	var zero T
	if err := ref.Validate(); err != nil {
		return zero, err
	}
	if c != nil {
		if e, ok := c.entries[ref]; ok {
			return e.Implementation, nil
		}
	}
	return zero, fmt.Errorf("EXTENSION_UNAVAILABLE: install exact %s@%s digest %s", ref.ID, ref.Version, ref.Digest)
}

// Configure validates against the pinned descriptor before returning an
// implementation. Configuration is data and cannot register executable code.
func (c *Catalog[T]) Configure(ref extension.Ref, config json.RawMessage, validator valueschema.Validator) (T, error) {
	impl, err := c.Resolve(ref)
	if err != nil {
		return impl, err
	}
	var zero T
	if validator == nil {
		return zero, fmt.Errorf("EXTENSION_VALIDATOR_REQUIRED")
	}
	if err := validator.Validate(c.entries[ref].Descriptor.ConfigurationSchema, config); err != nil {
		return zero, fmt.Errorf("EXTENSION_CONFIGURATION: %w", err)
	}
	return impl, nil
}

func (c *Catalog[T]) Descriptors() []extension.Descriptor {
	result := []extension.Descriptor{}
	if c != nil {
		for _, e := range c.entries {
			result = append(result, clone(e.Descriptor))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].Ref, result[j].Ref
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Version < b.Version
	})
	return result
}

func clone(d extension.Descriptor) extension.Descriptor {
	d.ConfigurationSchema = append(json.RawMessage(nil), d.ConfigurationSchema...)
	d.RequiredCapabilities = append([]string(nil), d.RequiredCapabilities...)
	return d
}
