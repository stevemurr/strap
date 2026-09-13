package chatwire

import (
	"strings"
	"testing"
)

const done = "data: [DONE]\n\n"

// A streamed response is validated as it arrives, so a server that contradicts
// itself is reported rather than assembled into a plausible-looking answer.
func TestStreamRejectsContradictoryChunks(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"invalid UTF-8", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\xff\xfe\"}}]}\n\n" + done},
		{"provider error", `data: {"error":"upstream exploded"}` + "\n\n" + done},
		{"choice at a non-zero index", `data: {"choices":[{"index":3,"delta":{"content":"hi"}}]}` + "\n\n" + done},
		{"more than one choice", `data: {"choices":[{"index":0,"delta":{"content":"a"}},{"index":1,"delta":{"content":"b"}}]}` + "\n\n" + done},
		{"choice after finish", `data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{"content":"more"}}]}` + "\n\n" + done},
		{"non-assistant role", `data: {"choices":[{"index":0,"delta":{"role":"user","content":"a"}}]}` + "\n\n" + done},
		{"conflicting role", `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"a"}}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{"role":"system","content":"b"}}]}` + "\n\n" + done},
		{"negative tool index", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":-1,"id":"a","function":{"name":"read"}}]}}]}` + "\n\n" + done},
		{"tool index gap", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":5,"id":"a","function":{"name":"read"}}]}}]}` + "\n\n" + done},
		{"conflicting tool ID", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"b"}]}}]}` + "\n\n" + done},
		{"conflicting tool type", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"magic"}]}}]}` + "\n\n" + done},
		{"malformed chunk", "data: {\n\n" + done},
	}
	for _, tc := range cases {
		if _, err := readStream(strings.NewReader(tc.input), nil); err == nil {
			t.Fatal("accepted a contradictory stream:", tc.name)
		}
	}
}

// Tool arguments are bounded, so a server cannot stream an unbounded call.
func TestStreamBoundsToolArguments(t *testing.T) {
	var b strings.Builder
	b.WriteString(`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read","arguments":"{"}}]}}]}` + "\n\n")
	chunk := strings.Repeat("x", 64<<10)
	for i := 0; i < 200; i++ {
		b.WriteString(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + chunk + `"}}]}}]}` + "\n\n")
	}
	b.WriteString(done)
	if _, err := readStream(strings.NewReader(b.String()), nil); err == nil {
		t.Fatal("accepted unbounded tool arguments")
	}
}
