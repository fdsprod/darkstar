// Package valueschema defines validation of user-authored JSON Schemas.
package valueschema

import "encoding/json"

type Validator interface {
	Validate(schema, value json.RawMessage) error
}
