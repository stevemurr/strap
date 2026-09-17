package tui

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
)

// The eval activity renderer receives facts, never a live session handle. Even
// an accidentally forwarded composer/control command cannot affect execution.
type evalSession struct{ root message.ActorID }

var errEvalReadOnly = errors.New("eval activity is read only")

func (s evalSession) Root() message.ActorID        { return s.root }
func (s evalSession) Agents() []harness.AgentInfo  { return nil }
func (s evalSession) AutomaticContextTokens() bool { return true }
func (s evalSession) NextEvent(context.Context) (conversation.Event, error) {
	return nil, errEvalReadOnly
}
func (s evalSession) Send(message.ActorID, string) (message.Receipt, error) {
	return message.Receipt{}, errEvalReadOnly
}
func (s evalSession) InspectAgent(message.ActorID, conversation.InspectOptions) (harness.AgentInspection, error) {
	return harness.AgentInspection{}, errEvalReadOnly
}
func (s evalSession) PauseAgent(message.ActorID) (conversation.AgentInfo, error) {
	return conversation.AgentInfo{}, errEvalReadOnly
}
func (s evalSession) ResumeAgent(message.ActorID) (conversation.AgentInfo, error) {
	return conversation.AgentInfo{}, errEvalReadOnly
}
func (s evalSession) Interrupt(context.Context) error { return errEvalReadOnly }

func (s evalSession) StopAgent(message.ActorID) (conversation.AgentInfo, error) {
	return conversation.AgentInfo{}, errEvalReadOnly
}

func newEvalActivity(ctx context.Context, root message.ActorID) *model {
	a := newModel(ctx, func() {}, evalSession{root: root}, Options{})
	a.entries = nil // The eval host supplies the task, not the conversational welcome.
	a.input.Blur()
	a.selectStream("")
	return a
}
