package replay_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stevemurr/strap/harness/replay"
	"github.com/stevemurr/strap/provider"
)

// Which role the manager creates is its dispatch decision, so the outcome
// names it.
func TestClassifyNamesTheCreatedRole(t *testing.T) {
	req := provider.Request{Tools: []provider.ToolDefinition{{Name: "create_agent"}, {Name: "send_message"}}}
	for _, c := range []struct {
		resp provider.Response
		err  error
		want string
	}{
		{provider.Response{ToolCalls: []provider.ToolCall{{Name: "create_agent", Arguments: json.RawMessage(`{"input": {"role": "web_researcher"}}`)}}}, nil, "call create_agent(web_researcher)"},
		{provider.Response{ToolCalls: []provider.ToolCall{{Name: "create_agent", Arguments: json.RawMessage(`{"input": {}}`)}, {Name: "send_message"}}}, nil, "call create_agent,send_message"},
		{provider.Response{Content: "I'll call send_message(x)"}, nil, "text send_message"},
		{provider.Response{Content: "SQLite 3.35.0 added RETURNING."}, nil, "text"},
		{provider.Response{}, errors.New("boom"), "error"},
	} {
		if got := replay.Classify(req, c.resp, c.err); got != c.want {
			t.Errorf("Classify = %q, want %q", got, c.want)
		}
	}
}
