package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"darkstar/src/ports/extension"
	"darkstar/src/ports/outputvalidator"
	"darkstar/src/ports/valueschema"
)

type Check struct {
	Ref           extension.Ref   `json:"ref"`
	Configuration json.RawMessage `json:"configuration"`
}

type ValidationEvidence struct {
	Validator       extension.Ref `json:"validator"`
	CandidateDigest string        `json:"candidateDigest"`
	Diagnostics     []string      `json:"diagnostics"`
}

// Validate executes in declaration order and stops on the first failed check.
// Evidence is returned to the host for the same durable candidate commit.
func Validate(ctx context.Context, catalog *Catalog[outputvalidator.Validator], schema valueschema.Validator, checks []Check, inputs, outputs map[string]json.RawMessage) ([]ValidationEvidence, error) {
	encoded, err := json.Marshal(outputs)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	evidence := []ValidationEvidence{}
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			return evidence, err
		}
		validator, err := catalog.Configure(check.Ref, check.Configuration, schema)
		if err != nil {
			return evidence, err
		}
		if validator == nil {
			return evidence, fmt.Errorf("EXTENSION_UNAVAILABLE: validator %s", check.Ref.ID)
		}
		// Each validator receives independent values, so it cannot rewrite the
		// candidate or affect what the next check sees.
		diagnostics, err := validator.Validate(ctx, outputvalidator.Request{Configuration: append(json.RawMessage(nil), check.Configuration...), Inputs: copyValues(inputs), Outputs: copyValues(outputs)})
		evidence = append(evidence, ValidationEvidence{check.Ref, digest, diagnostics})
		if err != nil {
			return evidence, fmt.Errorf("validator %s: %w", check.Ref.ID, err)
		}
	}
	return evidence, nil
}

func copyValues(values map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(values))
	for k, v := range values {
		result[k] = append(json.RawMessage(nil), v...)
	}
	return result
}
