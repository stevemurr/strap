package machine

import (
	"encoding/json"
	"testing"

	"github.com/stevemurr/strap/conversation"
)

// A write whose record shows the workspace was compared and nothing changed
// wrote somewhere else: an auditor's or experimenter's own copy. Only records
// from before the comparison was noted fall back to the argument path.
func TestRecorderTakesWritesFromTheWorkspaceComparison(t *testing.T) {
	args := json.RawMessage(`{"input":{"path":"text_test.go","content":"package text"}}`)
	for _, c := range []struct {
		name  string
		event conversation.EnvironmentEvent
		want  int
	}{
		{"write to a copy", conversation.EnvironmentEvent{Agent: aud, InvocationID: "1", Name: "write_file", Arguments: args, Scanned: true}, 0},
		{"write to the workspace", conversation.EnvironmentEvent{Agent: impl, InvocationID: "2", Name: "write_file", Arguments: args, Scanned: true, Changed: []string{"text_test.go"}}, 1},
		{"record without the comparison", conversation.EnvironmentEvent{Agent: impl, InvocationID: "3", Name: "write_file", Arguments: args}, 1},
	} {
		r := NewRecorder(mgr)
		r.Observe(0, c.event)
		if got := len(r.Writes); got != c.want {
			t.Errorf("%s: %d writes, want %d", c.name, got, c.want)
		}
	}
}
