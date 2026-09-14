package statestore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// RepositoryScopeDigest excludes its own digest field; all frozen repository,
// configuration, membership and source-selection facts remain covered.
func RepositoryScopeDigest(scope RepositoryScope) string {
	scope.Digest = ""
	return RepositoryScopeContentDigest(scope)
}

func RepositoryScopeBindingDigest(binding RepositoryScopeAttemptBinding) string {
	binding.Digest = ""
	return RepositoryScopeContentDigest(binding)
}

func RepositoryScopeContentDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
