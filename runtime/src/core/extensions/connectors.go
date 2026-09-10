package extensions

import (
	"context"
	"fmt"

	"darkstar/src/ports/delivery"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/worksource"
)

// Delivery catalogs preserve the existing narrow operation ports: selecting a
// connector does not authorize publication. The daemon issues operation IDs,
// obtains delivery approval, and reconciles effects through those ports.
func NewDeliveryCatalog(entries ...Registration[delivery.Connector]) (*Catalog[delivery.Connector], error) {
	for _, e := range entries {
		if e.Implementation == nil {
			return nil, fmt.Errorf("delivery connector is required")
		}
	}
	return New(entries...)
}

func NewSourceCatalog(entries ...Registration[worksource.Source]) (*Catalog[worksource.Source], error) {
	for _, e := range entries {
		if e.Implementation == nil {
			return nil, fmt.Errorf("work source is required")
		}
	}
	return New(entries...)
}

// FetchSource normalizes source observations before an application imports
// them. Provider content is evidence, never workflow instructions or authority.
func FetchSource(ctx context.Context, catalog *Catalog[worksource.Source], ref extension.Ref, request worksource.Request) (worksource.Observation, error) {
	if request.Reference == "" {
		return nil, fmt.Errorf("source reference is required")
	}
	source, err := catalog.Resolve(ref)
	if err != nil {
		return nil, err
	}
	value, err := source.Fetch(ctx, request)
	if err != nil {
		return nil, err
	}
	switch v := value.(type) {
	case worksource.Item:
		if v.Reference == request.Reference && v.Revision != "" && v.Title != "" {
			return v, nil
		}
	case worksource.Unchanged:
		if request.Revision != "" && v.Revision == request.Revision {
			return v, nil
		}
	case worksource.Missing:
		if v.Reference == request.Reference {
			return v, nil
		}
	}
	return nil, fmt.Errorf("SOURCE_INVALID_OBSERVATION: result is not bound to the requested source")
}
