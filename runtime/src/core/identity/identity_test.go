package identity

import "testing"

func TestEncode128PreservesLowOrderBits(t *testing.T) {
	zero := make([]byte, 16)
	lowBit := make([]byte, 16)
	lowBit[15] = 1

	if got, want := encode128(zero), "00000000000000000000000000"; got != want {
		t.Fatalf("encode zero = %q, want %q", got, want)
	}
	if got, want := encode128(lowBit), "00000000000000000000000001"; got != want {
		t.Fatalf("encode low bit = %q, want %q", got, want)
	}
}
