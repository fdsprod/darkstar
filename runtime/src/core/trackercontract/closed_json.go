package trackercontract

// DecodeClosedJSON shares the observation codec's bounded, duplicate-rejecting
// decoding rules with adjacent versioned tracker persistence envelopes.
func DecodeClosedJSON(encoded []byte, result any) error {
	return strictJSON(encoded, result)
}
