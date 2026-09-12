package agent_test

import (
	"context"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"sync"
	"testing"
)

type streamFunc func(context.Context, provider.Request, provider.Observer) (provider.Response, error)

func (f streamFunc) Submit(c context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
	return f(c, r, o)
}
func TestFailedOutputRetainsPartialTextWithoutHistoryCommit(t *testing.T) {
	c := config()
	var mu sync.Mutex
	var facts []agent.Event
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		mu.Lock()
		defer mu.Unlock()
		facts = append(facts, e)
		return nil
	})
	sentinel := errors.New("upstream failed")
	c.Spec.Provider = streamFunc(func(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
		if err := o.OnDelta(provider.Delta{Text: "partial ✓"}); err != nil {
			return provider.Response{}, err
		}
		return provider.Response{}, sentinel
	})
	a := mustAgent(t, c)
	_ = c.Inbox.Send(message.Message{ID: "input", From: message.User, To: c.ID, Content: "go"})
	if err := a.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	page, err := a.Transcript(agent.TranscriptQuery{Limit: 100})
	if err != nil || len(page.Entries) != 2 {
		t.Fatal(page, err)
	}
	var text string
	var finish agent.OutputFinished
	for _, e := range facts {
		switch v := e.(type) {
		case agent.OutputDelta:
			if v.Offset != uint64(len(text)) {
				t.Fatal(v)
			}
			text += v.Text
		case agent.OutputFinished:
			finish = v
		}
	}
	if text != "partial ✓" || finish.Status != agent.OutputFailed || finish.Bytes != uint64(len(text)) || finish.HistoryPosition != nil {
		t.Fatal(text, finish)
	}
}
func TestTimerPublicationFailureCancelsSilentProvider(t *testing.T) {
	c := config()
	sentinel := errors.New("disk failed")
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		if _, ok := e.(agent.OutputDelta); ok {
			return sentinel
		}
		return nil
	})
	c.Spec.Provider = streamFunc(func(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
		if err := o.OnDelta(provider.Delta{Text: "pending"}); err != nil {
			return provider.Response{}, err
		}
		<-ctx.Done()
		return provider.Response{Content: "pending"}, nil
	})
	a := mustAgent(t, c)
	_ = c.Inbox.Send(message.Message{ID: "input", From: message.User, To: c.ID, Content: "go"})
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	if err := await(t, done); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	p, _ := a.Transcript(agent.TranscriptQuery{Limit: 100})
	if len(p.Entries) != 2 {
		t.Fatal(p)
	}
}

func TestStopCancelsProviderWhileDeltaPublicationIsBlocked(t *testing.T) {
	c := config()
	entered, release, providerCanceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		if _, ok := e.(agent.OutputDelta); ok {
			close(entered)
			<-release
		}
		return nil
	})
	c.Spec.Provider = streamFunc(func(ctx context.Context, _ provider.Request, o provider.Observer) (provider.Response, error) {
		if err := o.OnDelta(provider.Delta{Text: "pending"}); err != nil {
			return provider.Response{}, err
		}
		<-ctx.Done()
		close(providerCanceled)
		return provider.Response{}, ctx.Err()
	})
	a := mustAgent(t, c)
	_ = c.Inbox.Send(message.Message{ID: "input", From: message.User, To: c.ID, Content: "go"})
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	await(t, entered)
	stopped := make(chan struct{})
	go func() { a.RequestStop(); close(stopped) }()
	await(t, providerCanceled)
	close(release)
	await(t, stopped)
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
