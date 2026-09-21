package agent

import (
	"context"
	"testing"

	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

type revisionProvider struct{}

func (revisionProvider) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{}, nil
}

type revisionSender struct{}

func (revisionSender) Send(context.Context, message.Draft) (message.Receipt, error) {
	return message.Receipt{}, nil
}
func TestLifecycleRevisionIsIndependentFromHistory(t *testing.T) {
	var events []StateSnapshot
	a, err := New(Config{ID: "a", Spec: Spec{Provider: revisionProvider{}}, Inbox: inbox.New[message.Message](), Outbox: revisionSender{}, Reporter: ReporterFunc(func(_ context.Context, e Event) error {
		if s, ok := e.(StateSnapshot); ok {
			events = append(events, s)
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	history := a.ContextRevision()
	if _, err = a.PauseSnapshot(); err != nil {
		t.Fatal(err)
	}
	first := a.StateSnapshot()
	if _, err = a.PauseSnapshot(); err != nil {
		t.Fatal(err)
	}
	if a.StateSnapshot() != first {
		t.Fatal("idempotent pause changed revision")
	}
	if _, err = a.ResumeSnapshot(); err != nil {
		t.Fatal(err)
	}
	last := a.StateSnapshot()
	if last.Revision != first.Revision+1 || last.State != Running || a.ContextRevision() != history {
		t.Fatal(first, last, a.ContextRevision())
	}
	if len(events) != 2 || events[0] != first || events[1] != last {
		t.Fatal(events)
	}
}
