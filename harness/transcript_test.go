package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func TestInspectionProjectionIsBoundedAndDoesNotMutateTranscript(t *testing.T) {
	entries := make([]agent.TranscriptEntry, 100)
	for i := range entries {
		entries[i] = agent.TranscriptEntry{Position: uint64(i + 1), Message: provider.Message{Role: "tool", ToolCallID: "call-1", Content: content.Text(strings.Repeat("\"世界", 3000))}}
	}
	in := conversation.AgentInspection{AgentInfo: conversation.AgentInfo{ID: "root"}, Transcript: &agent.TranscriptPage{Entries: entries}}
	result, err := inspectionResult(AgentInspection{AgentInspection: in})
	if err != nil {
		t.Fatal(err)
	}
	raw := result.Content.Text()
	if len(raw) > 34*1024 || !utf8.ValidString(raw) {
		t.Fatal("unbounded or malformed response")
	}
	var out struct {
		Transcript inspectionPage `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Transcript.HasEarlier || len(out.Transcript.Entries) == 0 {
		t.Fatal("missing paging indicator")
	}
	for _, entry := range out.Transcript.Entries {
		if !entry.Truncated {
			t.Fatal("shortened content not marked")
		}
	}
	if out.Transcript.Entries[len(out.Transcript.Entries)-1].Position != 100 {
		t.Fatal("did not retain latest entries")
	}
	if in.Transcript.Entries[0].Message.Content.Text() != strings.Repeat("\"世界", 3000) {
		t.Fatal("projection mutated original")
	}
}

func TestInspectionProjectionPreservesRawArgumentsAndLabelsImages(t *testing.T) {
	in := conversation.AgentInspection{Transcript: &agent.TranscriptPage{Entries: []agent.TranscriptEntry{{Position: 1, Message: provider.Message{Role: "assistant", Content: content.Content{{Text: "checking"}, {Image: &content.Image{MIMEType: "image/png", Data: []byte("secret binary")}}}, ToolCalls: []provider.ToolCall{{ID: "bad", Name: "shell", Arguments: json.RawMessage(`{"invalid":`)}}}}}}}
	result, err := inspectionResult(AgentInspection{AgentInspection: in})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Transcript inspectionPage `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(result.Content.Text()), &out); err != nil {
		t.Fatal(err)
	}
	text := out.Transcript.Entries[0].Text
	if !strings.Contains(text, `{"invalid":`) || !strings.Contains(text, "binary data omitted") || strings.Contains(text, "secret binary") {
		t.Fatal(text)
	}
}

func TestInspectToolDefaultsToTranscriptAndPreservesThread(t *testing.T) {
	c := conversation.New(context.Background())
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	created, err := c.CreateAgent(message.User, agent.Spec{Provider: unusedProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := inspectTool(runtimeFixture{c}).Call(context.Background(), tool.Call{Actor: created.AgentID, Arguments: json.RawMessage(`{"agent_id":"` + string(created.AgentID) + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Transcript inspectionPage `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(result.Content.Text()), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Transcript.Entries) != 1 || out.Transcript.Entries[0].Role != "system" {
		t.Fatal("default inspection missing actual thread")
	}
	in, err := c.InspectAgent(created.AgentID, conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
	if err != nil || len(in.Transcript.Entries) != 1 {
		t.Fatal("direct read changed target thread")
	}
}
