package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolArgumentsError retains a rejected call for diagnostics only. Arguments is
// the complete decoded protocol string, before whitespace trimming or JSON
// validation. It is a string, not json.RawMessage, so malformed JSON can be
// recorded without breaking the enclosing record. It must never be dispatched
// or appended to model history as an accepted tool call.
//
// FinishReason, Index and Calls describe the response the call arrived in, so
// a trace can show whether generation ended normally and whether further calls
// followed the rejected one.
type ToolArgumentsError struct {
	CallID       string `json:"call_id"`
	Name         string `json:"name"`
	Arguments    string `json:"arguments"`
	FinishReason string `json:"finish_reason,omitempty"`
	Index        int    `json:"index"`
	Calls        int    `json:"calls"`
}

func (e *ToolArgumentsError) Error() string {
	args := strings.TrimSpace(e.Arguments)
	switch {
	case args == "":
		return fmt.Sprintf("tool %s call has no arguments", e.Name)
	case !json.Valid([]byte(args)):
		return fmt.Sprintf("tool %s arguments are incomplete JSON (%d bytes, call %d of %d, finish_reason %q); the model did not close the tool call", e.Name, len(args), e.Index+1, e.Calls, e.FinishReason)
	default:
		return fmt.Sprintf("tool %s arguments must be a JSON object", e.Name)
	}
}
