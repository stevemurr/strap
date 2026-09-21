package eval

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/message"
)

type Phase string

const (
	Queued   Phase = "queued"
	Starting Phase = "starting"
	Running  Phase = "running"
	Grading  Phase = "grading"
	Finished Phase = "finished"
)

// Progress is an observation, never a control surface. Events for a task are
// ordered; different workers may call Observe concurrently. Observers must
// return promptly and must not mutate the event or result.
type Progress struct {
	Task   Task
	Phase  Phase
	At     time.Time
	Root   message.ActorID
	Event  conversation.Event
	Result *Result
}

func (o Options) notify(p Progress) {
	if o.Observe != nil {
		p.At = time.Now()
		o.Observe(p)
	}
}

// Observe through an independent cursor so rendering cannot steal events from
// the completion watcher. Drain final telemetry before storage is disposed.
func (o Options) observeSession(session *harness.Session, task Task) func(context.Context) {
	if o.Observe == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	root := session.Root()
	notice := func(err error) {
		o.notify(Progress{Task: task, Root: root, Event: conversation.DiagnosticEvent{Level: "warn", Message: "Live observation: " + err.Error()}})
	}
	sub, err := session.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		cancel()
		notice(err)
		return func(context.Context) {}
	}
	o.notify(Progress{Task: task, Phase: Running, Root: root})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer sub.Close()
		for {
			e, err := sub.Next(ctx)
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
					notice(err)
				}
				return
			}
			// History records can contain large copies of model messages. The
			// live renderer uses streamed output/message facts instead.
			if e.Kind == "content_chunk" || e.Kind == "history_appended" || e.Kind == "inbox_disposition" || e.Kind == "agent_yielded" {
				continue
			}
			e, err = session.ResolveRecord(ctx, e)
			if err != nil {
				notice(err)
				continue
			}
			v, err := eventcodec.DecodeEvent(e)
			if err != nil {
				notice(err)
				continue
			}
			if v != nil {
				o.notify(Progress{Task: task, Root: root, Event: v})
			}
		}
	}()
	return func(wait context.Context) {
		defer cancel()
		select {
		case <-done:
		case <-wait.Done():
			cancel()
			<-done
		}
	}
}
