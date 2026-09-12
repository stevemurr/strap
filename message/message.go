// Package message defines agent communication and structured delivery receipts.
package message

import (
	"context"
	"errors"
	"strings"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

type ActorID = identity.ActorID
type MessageID = identity.MessageID

const User ActorID = "user"

type MessageKind string

const (
	Instruction  MessageKind = "instruction"
	Reply        MessageKind = "reply"
	Failure      MessageKind = "failure"
	Notification MessageKind = "notification"
	Observation  MessageKind = "observation"
)

// Message is an immutable, controller-addressed envelope. A user's message is
// input to the conversation, not a new execution owner.
type Message struct {
	Output  *identity.OutputID `json:"-"`
	ID      MessageID          `json:"id"`
	From    ActorID            `json:"from"`
	To      ActorID            `json:"to"`
	Kind    MessageKind        `json:"kind"`
	ReplyTo MessageID          `json:"reply_to,omitempty"`
	Content string             `json:"content,omitempty"`
	Work    *work.Work         `json:"work,omitempty"`
	Event   *work.Event        `json:"event,omitempty"`
}

type DeliveryStatus string

const (
	Queued      DeliveryStatus = "queued"
	Consumed    DeliveryStatus = "consumed"
	Undelivered DeliveryStatus = "undelivered"
)

// Receipt acknowledges routing. Consumed means appended to the recipient's
// context, not adopted, agreed with, or completed.
type Receipt struct {
	MessageID MessageID      `json:"message_id"`
	Recipient ActorID        `json:"recipient"`
	Status    DeliveryStatus `json:"status"`
	Detail    string         `json:"detail,omitempty"`
}

// Draft leaves sender identity and message identity to the controller.
type Draft struct {
	Output  *identity.OutputID // Host correlation; never encoded in the model envelope.
	To      ActorID
	Kind    MessageKind
	ReplyTo MessageID
	Content string
	Work    *work.Work
	Event   *work.Event
}

// Sender is bound to one actor. All agent-to-agent messages use this path.
type Sender interface {
	Send(context.Context, Draft) (Receipt, error)
}

// Clone gives the recipient its own structured payload.
func (m Message) Clone() Message {
	if m.Output != nil {
		v := *m.Output
		m.Output = &v
	}
	if m.Work != nil {
		snapshot := m.Work.Clone()
		m.Work = &snapshot
	}
	if m.Event != nil {
		event := m.Event.Clone()
		m.Event = &event
	}
	return m
}

// Validate checks the payload before it is routed.
func (d Draft) Validate() error {
	if d.Event != nil {
		if d.Work != nil || d.Content != "" || (d.Kind != Notification && d.Kind != Observation) {
			return errors.New("event requires a notification or observation envelope")
		}
		return nil
	}
	if d.Work != nil {
		if d.Content != "" {
			return errors.New("message must contain content or work, not both")
		}
		if d.Kind != Instruction {
			return errors.New("work must be instruction messages")
		}
		if strings.TrimSpace(d.Work.Task) == "" {
			return errors.New("work task is required")
		}
		return nil
	}
	if d.Content == "" {
		return errors.New("message content is empty")
	}
	return nil
}
