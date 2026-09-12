package projection_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
)

func TestSchema2ArchiveNormalizesContentWithoutRewritingRecords(t *testing.T) {
	ctx := context.Background()
	id := identity.OutputID{Agent: "agent", Call: 1}
	data := []eventlog.Data{{Kind: "session_started", Payload: json.RawMessage(`{"id":"session"}`)}}
	for _, e := range []conversation.Event{
		conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "agent", Parent: "user", State: agent.Idle, StateRevision: 1}},
		conversation.AgentEvent{Agent: "agent", Event: agent.HistoryAppended{Position: 1, Message: provider.Message{Role: "system"}}},
		conversation.AgentEvent{Agent: "agent", Event: agent.OutputStarted{Output: id, ContextRevision: 1, StartedAt: time.Now()}},
		conversation.AgentEvent{Agent: "agent", Event: agent.OutputDelta{Output: id, Text: "old answer"}},
	} {
		d, err := eventcodec.EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind == "output_delta" {
			var payload map[string]any
			if err = json.Unmarshal(d.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			delete(payload, "channel")
			d.Payload, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
		}
		data = append(data, d)
	}
	var raw bytes.Buffer
	for i, d := range data {
		e := eventlog.Record{Schema: 2, Session: "session", Sequence: uint64(i + 1), Data: d}
		if err := json.NewEncoder(&raw).Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := os.WriteFile(path, raw.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.OpenJSONL(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	page, err := store.Read(ctx, eventlog.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	p := projection.New("session")
	for _, e := range page.Events {
		if e.Schema != 2 {
			t.Fatal("rewrote archive schema")
		}
		if err = p.Apply(e); err != nil {
			t.Fatal(err)
		}
		v, err := eventcodec.DecodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind == "output_delta" && v.(conversation.AgentEvent).Event.(agent.OutputDelta).Channel != provider.ChannelContent {
			t.Fatal(v)
		}
	}
	output, err := p.Output(id)
	if err != nil || output.TextBytes != 10 || output.ReasoningBytes != 0 {
		t.Fatal(output, err)
	}
	last := page.Events[len(page.Events)-1]
	last.Schema = 3
	last.Sequence++
	cursor := p.Cursor()
	if err = p.Apply(last); err == nil || p.Cursor() != cursor {
		t.Fatal("accepted missing schema-3 channel", err)
	}
	if _, err = eventcodec.DecodeEvent(last); err == nil {
		t.Fatal("decoded missing schema-3 channel")
	}
	last.Payload = json.RawMessage(`{"output":{"agent":"agent","call":1},"channel":"unknown","offset":10,"text":"bad"}`)
	if err = p.Apply(last); err == nil || p.Cursor() != cursor {
		t.Fatal("accepted unknown channel", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, raw.Bytes()) {
		t.Fatal("modified archive", err)
	}
}
