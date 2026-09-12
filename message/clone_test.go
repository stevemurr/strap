package message_test

import (
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
	"testing"
)

func TestCloneOwnsWorkAndEvent(t *testing.T) {
	original := message.Message{Work: &work.Work{Task: "task", Scope: &work.Scope{StepIDs: []work.StepID{"step"}}}, Event: &work.Event{Work: work.Work{Task: "event", Scope: &work.Scope{StepIDs: []work.StepID{"event-step"}}}}}
	copied := original.Clone()
	copied.Work.Scope.StepIDs[0] = "changed"
	copied.Event.Work.Scope.StepIDs[0] = "changed"
	if original.Work.Scope.StepIDs[0] != "step" || original.Event.Work.Scope.StepIDs[0] != "event-step" {
		t.Fatal("clone aliases structured payload")
	}
	if (message.Message{}).Clone().Work != nil {
		t.Fatal("empty clone")
	}
}
func TestEventEnvelopeValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		draft   message.Draft
		invalid bool
	}{
		{"notification", message.Draft{Kind: message.Notification, Event: &work.Event{}}, false},
		{"observation", message.Draft{Kind: message.Observation, Event: &work.Event{}}, false},
		{"instruction", message.Draft{Kind: message.Instruction, Event: &work.Event{}}, true},
		{"content", message.Draft{Kind: message.Notification, Event: &work.Event{}, Content: "text"}, true},
		{"work", message.Draft{Kind: message.Notification, Event: &work.Event{}, Work: &work.Work{}}, true},
		{"empty", message.Draft{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.draft.Validate(); (err != nil) != tc.invalid {
				t.Fatal(err)
			}
		})
	}
}
