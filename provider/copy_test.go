package provider_test

import (
	"encoding/json"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"testing"
)

func TestCopiesOwnMutablePayloads(t *testing.T) {
	original := []provider.Message{{Role: "assistant", Content: content.Content{{Image: &content.Image{MIMEType: "image/png", Data: []byte{1}}}}, ToolCalls: []provider.ToolCall{{ID: "call", Arguments: json.RawMessage(`{"x":1}`)}}, Envelope: &message.Message{Content: "hello"}}}
	copied := provider.CopyMessages(original)
	copied[0].Role = "user"
	copied[0].Content[0].Image.Data[0] = 9
	copied[0].ToolCalls[0].Arguments[0] = '!'
	copied[0].Envelope.Content = "changed"
	if original[0].Role != "assistant" || original[0].Content[0].Image.Data[0] != 1 || string(original[0].ToolCalls[0].Arguments) != `{"x":1}` || original[0].Envelope.Content != "hello" {
		t.Fatal("copy mutated original", original)
	}
	if provider.CopyMessages(nil) != nil || provider.CopyCalls(nil) != nil {
		t.Fatal("nil copies should remain nil")
	}
}
func TestUsageClonePreservesMissingAndZeroCounts(t *testing.T) {
	if (*provider.Usage)(nil).Clone() != nil {
		t.Fatal("nil usage")
	}
	for _, original := range []*provider.Usage{{}, {InputTokens: tokenCount(0)}, {OutputTokens: tokenCount(7)}, {InputTokens: tokenCount(3), OutputTokens: tokenCount(4)}} {
		copied := original.Clone()
		if copied == original || (copied.InputTokens == nil) != (original.InputTokens == nil) || (copied.OutputTokens == nil) != (original.OutputTokens == nil) {
			t.Fatal("invalid clone")
		}
		if copied.InputTokens != nil {
			want := *original.InputTokens
			*copied.InputTokens = 99
			if *original.InputTokens != want {
				t.Fatal("input alias")
			}
		}
		if copied.OutputTokens != nil {
			want := *original.OutputTokens
			*copied.OutputTokens = 99
			if *original.OutputTokens != want {
				t.Fatal("output alias")
			}
		}
	}
}
