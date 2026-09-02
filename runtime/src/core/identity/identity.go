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
	result := make([]byte, 26)
	result[0] = crockford[(value[0]&0xe0)>>5]
	bit := 3
	for index := 1; index < len(result); index++ {
		var digit byte
		for offset := 0; offset < 5; offset++ {
			position := bit + offset
			digit <<= 1
			if position < 128 {
				digit |= (value[position/8] >> (7 - uint(position%8))) & 1
			}
		}
		result[index] = crockford[digit]
		bit += 5
	}
	return string(result)
}
