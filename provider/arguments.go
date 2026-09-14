package provider

import "fmt"

// ToolArgumentsError retains a rejected call for diagnostics only. Arguments is
// the complete decoded protocol string, before whitespace trimming or JSON
// validation. It is a string, not json.RawMessage, so malformed JSON can be
// recorded without breaking the enclosing record. It must never be dispatched
// or appended to model history as an accepted tool call.
type ToolArgumentsError struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (e *ToolArgumentsError) Error() string {
	return fmt.Sprintf("tool %s arguments must be a JSON object", e.Name)
}
