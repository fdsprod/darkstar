// Package ports contains shared, application-owned vocabulary for runtime ports.
//
// Individual interfaces live in focused child packages: provider, artifactstore,
// delivery, contentprocessor, platform, and executor. Those packages may depend
// only on the standard library and this package. They must not expose concrete
// adapter, transport, provider SDK, database, or operating-system types.
package ports
