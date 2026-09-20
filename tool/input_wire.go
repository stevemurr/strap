package tool

import "encoding/json"

// Input is the single model-facing argument. Its value uses JSON in Qwen's XML
// format, retaining JSON string/null distinctions without root schema hints.
type Input[A any] struct {
	Value A `json:"input"`
}

// MarshalInput serializes a typed input; it does not fill absent fields.
func MarshalInput[A any](value A) (json.RawMessage, error) {
	return json.Marshal(Input[A]{Value: value})
}

func inputObjectNode(value *parameterNode) *parameterNode {
	return &parameterNode{kind: "object", fields: map[string]*parameterNode{"input": value}}
}
