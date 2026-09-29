package replay_test

import (
	"slices"
	"testing"

	"github.com/stevemurr/strap/harness/replay"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// An exchange takes only the messages recorded in it. In ladder easy-04 the
// manager settled message-11 alone, then message-12 and message-13 each began
// an exchange of their own; a running exchange that was handed the next ones
// waited for message-13, which needed the manager to go idle first.
func TestTakenKeepsEachExchangeToItsOwnMessages(t *testing.T) {
	const mgr = identity.ActorID("agent-2")
	envelope := func(id message.MessageID) provider.Message {
		return provider.Message{Role: "user", Envelope: &message.Message{ID: id}}
	}
	rec := &replay.Recording{
		Histories: map[identity.ActorID][]provider.Message{mgr: {
			{Role: "assistant"}, envelope("message-11"), envelope("message-12"), envelope("message-13"), envelope("message-14"),
		}},
		Consumed: map[identity.ActorID]map[message.MessageID]bool{mgr: {"message-11": true, "message-12": true, "message-13": true, "message-14": true}},
		// message-14 joined message-13's exchange before its admission.
		Starts: map[identity.ActorID]map[message.MessageID]bool{mgr: {"message-11": true, "message-12": true, "message-13": true}},
	}
	for _, c := range []struct {
		position uint64
		starting bool
		want     []message.MessageID
	}{
		{2, true, []message.MessageID{"message-11"}}, // message-11 begins its exchange alone.
		{3, false, nil}, // Its exchange takes nothing more,
		{3, true, []message.MessageID{"message-12"}},               // so message-12 begins the next,
		{4, true, []message.MessageID{"message-13", "message-14"}}, // and message-14 joins message-13's.
		{5, false, []message.MessageID{"message-14"}},              // A running exchange takes what joined it.
	} {
		got, ok := rec.Taken(mgr, c.position, c.starting)
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("Taken(%d, starting=%v) = %v, %v; want %v", c.position, c.starting, got, ok, c.want)
		}
	}
	// A trace without dispositions takes the whole run, as before.
	rec.Starts = nil
	if got, _ := rec.Taken(mgr, 3, false); !slices.Equal(got, []message.MessageID{"message-12", "message-13", "message-14"}) {
		t.Errorf("without dispositions: %v", got)
	}
}
