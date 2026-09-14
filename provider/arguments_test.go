package provider

import (
	"strings"
	"testing"
)

func TestToolArgumentsErrorNamesTheActualDefect(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{"", "tool write_file call has no arguments"},
		{" \n", "tool write_file call has no arguments"},
		{`{"path": "a", "content": "unterminated`, `tool write_file arguments are incomplete JSON (38 bytes, call 1 of 2, finish_reason "tool_calls"); the model did not close the tool call`},
		{`[]`, "tool write_file arguments must be a JSON object"},
		{`"text"`, "tool write_file arguments must be a JSON object"},
	}
	for _, c := range cases {
		err := &ToolArgumentsError{CallID: "c", Name: "write_file", Arguments: c.args, FinishReason: "tool_calls", Index: 0, Calls: 2}
		if got := err.Error(); got != c.want {
			t.Fatalf("%q: got %q, want %q", c.args, got, c.want)
		}
		if strings.Contains(err.Error(), c.args) && c.args != "" {
			t.Fatalf("message must not echo argument bytes: %q", err)
		}
	}
}
