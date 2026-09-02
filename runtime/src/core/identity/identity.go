// Package identity creates canonical opaque runtime identifiers.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Deterministic returns the same canonical identifier for the same prefix and seed.
func Deterministic(prefix, seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return prefix + encode128(digest[:16])
}

// Random returns a canonical identifier backed by 128 bits of randomness.
func Random(prefix string) string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic("cryptographic identity generation failed: " + err.Error())
	}
	return prefix + encode128(value)
}

func encode128(value []byte) string {
	encoded := make([]byte, 26)
	buffer, bits, position := uint32(0), uint(0), 0
	for _, item := range append([]byte{0}, value...) {
		buffer = buffer<<8 | uint32(item)
		bits += 8
		for bits >= 5 && position < len(encoded) {
			bits -= 5
			encoded[position] = crockford[(buffer>>bits)&31]
			position++
		}
	}
	return string(encoded)
}
