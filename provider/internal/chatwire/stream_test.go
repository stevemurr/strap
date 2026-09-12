package chatwire

import (
	"errors"
	"github.com/stevemurr/strap/provider"
	"strings"
	"testing"
)

func TestStreamAssemblesTextToolsAndUsage(t *testing.T) {
	input := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"hé"}}]}

data: {"choices":[{"index":0,"delta":{"content":"llo","tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read","arguments":"{\"x\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":8}}

data: [DONE]

`
	var chunks []string
	got, err := readStream(strings.NewReader(input), provider.ObserverFunc(func(d provider.Delta) error { chunks = append(chunks, d.Text); return nil }))
	if err != nil || got.Content != "héllo" || len(chunks) != 2 || len(got.ToolCalls) != 1 || string(got.ToolCalls[0].Arguments) != `{"x":1}` || *got.Usage.OutputTokens != 8 {
		t.Fatal(got, chunks, err)
	}
}
func TestStreamRejectsIncompleteAndPropagatesObserverError(t *testing.T) {
	input := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"
	sentinel := errors.New("recording failed")
	if _, err := readStream(strings.NewReader(input), provider.ObserverFunc(func(provider.Delta) error { return sentinel })); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := readStream(strings.NewReader(input), nil); err == nil {
		t.Fatal("accepted truncated stream")
	}
}
