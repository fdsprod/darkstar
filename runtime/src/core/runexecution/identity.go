package runexecution

import "darkstar/src/core/identity"

func stableID(prefix, seed string) string {
	return identity.Deterministic(prefix, seed)
}

func randomID(prefix string) string {
	return identity.Random(prefix)
}
