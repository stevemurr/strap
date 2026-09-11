package message_test

import (
	"encoding/json"
	"testing"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

func TestWorkRoundTrip(t *testing.T) {
	for _, want := range []work.Work{{Task: "a task"}, {Task: "a task", Context: "context\nwith newlines", ExpectedOutput: `a "result"`}} {
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var got work.Work
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestDraftPayloadValidation(t *testing.T) {
	valid := work.Work{Task: "work"}
	for _, draft := range []message.Draft{
		{},
		{Kind: message.Instruction, Work: &work.Work{Task: "\t"}},
		{Kind: message.Instruction, Content: "duplicate", Work: &valid},
		{Kind: message.Reply, Work: &valid},
	} {
		if err := draft.Validate(); err == nil {
			t.Fatalf("accepted %+v", draft)
		}
	}
	for _, draft := range []message.Draft{
		{Kind: message.Instruction, Work: &valid},
		{Kind: message.Instruction, Content: "follow-up"},
		{Kind: message.Reply, Content: "result"},
	} {
		if err := draft.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
