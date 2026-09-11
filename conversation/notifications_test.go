package conversation_test

import (
	"testing"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

func TestObservationsDoNotInvokeModelAndNotificationsPreserveReplyTarget(t *testing.T) {
	c, m := setup(t)
	observation := work.Event{ID: "progress", Kind: work.ProgressChanged, Work: work.Work{ID: "w", Task: "work"}}
	receipt, err := c.Deliver(message.User, message.Draft{To: c.Root(), Kind: message.Observation, Event: &observation})
	if err != nil {
		t.Fatal(err)
	}
	event(t, c, func(e conversation.Event) bool {
		a, ok := e.(conversation.AckEvent)
		return ok && a.Receipt.MessageID == receipt.MessageID && a.Receipt.Status == message.Consumed
	})
	select {
	case <-m.calls:
		t.Fatal("observation invoked model")
	default:
	}
	initial, err := c.Send(c.Root(), "Do the task")
	if err != nil {
		t.Fatal(err)
	}
	first := m.next(t)
	if first.request.Messages[1].Envelope.Event == nil {
		t.Fatal("observation missing from context")
	}
	first.text("Working")
	userReply(t, c, "Working")
	notification := work.Event{ID: "review", Kind: work.ReviewRequested, Work: work.Work{ID: "w", Task: "work"}, Actionable: true}
	_, err = c.Deliver(message.User, message.Draft{To: c.Root(), Kind: message.Notification, Event: &notification})
	if err != nil {
		t.Fatal(err)
	}
	second := m.next(t)
	second.text("Audit requested")
	reply := userReply(t, c, "Audit requested")
	if reply.ReplyTo != initial.MessageID {
		t.Fatalf("notification replaced request correlation: %+v", reply)
	}
}
func TestWorkEventSnapshotsDeepCopy(t *testing.T) {
	c, m := setup(t)
	e := work.Event{ID: "progress", Work: work.Work{Task: "work", Scope: &work.Scope{PlanID: "p", StepIDs: []work.StepID{"s"}}}, Steps: []work.Step{{ID: "s", AcceptanceCriteria: []string{"original"}}}}
	_, err := c.Deliver(message.User, message.Draft{To: c.Root(), Kind: message.Notification, Event: &e})
	if err != nil {
		t.Fatal(err)
	}
	e.Steps[0].AcceptanceCriteria[0] = "mutated"
	e.Work.Scope.StepIDs[0] = "mutated"
	call := m.next(t)
	got := call.request.Messages[1].Envelope.Event
	if got.Steps[0].AcceptanceCriteria[0] != "original" || got.Work.Scope.StepIDs[0] != "s" {
		t.Fatal("event snapshot aliases input")
	}
	call.text("seen")
}
