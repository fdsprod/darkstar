// Package trackerconnection provides protected credentials and immutable source
// evidence to read-only tracker adapters, without execution or writer authority.
package trackerconnection

import "context"

// CredentialResolver resolves a protected reference at request time, allowing
// credential rotation without storing secrets in configuration or adapter pins.
type CredentialResolver interface {
	Resolve(context.Context, string) (string, error)
}

// EvidenceStore retains an immutable, versioned provenance envelope containing
// original source bytes and their digest. It never replaces approved artifacts.
type EvidenceStore interface {
	Retain(context.Context, []byte) (string, error)
}
