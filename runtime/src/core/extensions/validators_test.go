package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"darkstar/src/ports/outputvalidator"
)

type acceptingSchema struct{}

func (acceptingSchema) Validate(json.RawMessage, json.RawMessage) error {
	return nil
}

type testValidator struct {
	calls *int
	fail  bool
}

func (v testValidator) Validate(_ context.Context, r outputvalidator.Request) ([]string, error) {
	*v.calls++
	r.Outputs["result"][1] = 'X'
	if v.fail {
		return []string{"rejected"}, errors.New("check failed")
	}
	return []string{"checked"}, nil
}

func TestValidatorsStopOnFailureAndCannotRewriteCandidate(t *testing.T) {
	calls := 0
	d := descriptor()
	e := d
	e.Ref.ID = "example/reject"
	c, err := New(Registration[outputvalidator.Validator]{d, testValidator{&calls, false}}, Registration[outputvalidator.Validator]{e, testValidator{&calls, true}})
	if err != nil {
		t.Fatal(err)
	}
	checks := []Check{{d.Ref, json.RawMessage(`{}`)}, {e.Ref, json.RawMessage(`{}`)}, {d.Ref, json.RawMessage(`{}`)}}
	outputs := map[string]json.RawMessage{"result": json.RawMessage(`"original"`)}
	evidence, err := Validate(context.Background(), c, acceptingSchema{}, checks, nil, outputs)
	if err == nil || calls != 2 || len(evidence) != 2 {
		t.Fatalf("calls=%d evidence=%v err=%v", calls, evidence, err)
	}
	if string(outputs["result"]) != `"original"` {
		t.Fatal("candidate was mutated")
	}
	if len(evidence[0].CandidateDigest) != 64 || evidence[0].CandidateDigest != evidence[1].CandidateDigest {
		t.Fatal("checks did not bind the same candidate")
	}
}

func TestRegistrationCannotGrantItsOwnCapabilities(t *testing.T) {
	d := descriptor()
	d.RequiredCapabilities = []string{"delivery.publish"}
	if _, err := New(Registration[int]{d, 1}); err == nil {
		t.Fatal("extension granted itself publication")
	}
}
