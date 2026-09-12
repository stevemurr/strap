package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

func count(n int64) *int64 { return &n }

func TestUsageSnapshotsAndEventsOwnCounts(t *testing.T) {
	a := &Agent{}
	if s := a.Usage(); s.Calls != 0 || s.Latest != nil {
		t.Fatal(s)
	}
	var events []UsageObservation
	a.config.OnUsage = func(o UsageObservation) {
		// The callback can read a committed snapshot without holding a lock.
		if a.Usage().Calls != o.Call {
			t.Error("event preceded accounting")
		}
		events = append(events, o)
	}
	u := &provider.Usage{InputTokens: count(100), OutputTokens: count(10)}
	a.recordUsage(2, u)
	*u.InputTokens = 999
	*events[0].Usage.OutputTokens = 999
	s := a.Usage()
	if *s.Latest.Usage.InputTokens != 100 || *s.Latest.Usage.OutputTokens != 10 {
		t.Fatal("provider or event aliases retained usage")
	}
	*s.Latest.Usage.InputTokens = 999
	if *a.Usage().Latest.Usage.InputTokens != 100 {
		t.Fatal("inspection aliases retained usage")
	}
	a.recordUsage(4, &provider.Usage{InputTokens: count(0)})
	a.recordUsage(6, nil)
	s = a.Usage()
	if s.Calls != 3 || s.InputTokens != 100 || s.OutputTokens != 10 || s.MissingInputCalls != 1 || s.MissingOutputCalls != 2 {
		t.Fatalf("incorrect totals: %+v", s)
	}
	if s.Latest.Call != 3 || s.Latest.ContextRevision != 6 || s.Latest.Usage != nil {
		t.Fatalf("latest observation is stale: %+v", s.Latest)
	}
}

func TestUsageConcurrentInspection(t *testing.T) {
	a := &Agent{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			a.recordUsage(uint64(i+1), &provider.Usage{InputTokens: count(10), OutputTokens: count(1)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s := a.Usage()
			if s.InputTokens != int64(s.Calls)*10 || s.OutputTokens != int64(s.Calls) || (s.Latest != nil && s.Latest.Call != s.Calls) {
				t.Errorf("inconsistent snapshot: %+v", s)
			}
			if s.Latest != nil {
				*s.Latest.Usage.InputTokens = -1
			}
		}
	}()
	wg.Wait()
	if s := a.Usage(); s.Calls != 500 || *s.Latest.Usage.InputTokens != 10 {
		t.Fatal(s)
	}
}

type usageProvider func(context.Context, provider.Request) (provider.Response, error)

func (f usageProvider) Submit(ctx context.Context, r provider.Request, observer provider.Observer) (provider.Response, error) {
	return f(ctx, r)
}

type unexpectedUsageSender struct{ t *testing.T }

func (s unexpectedUsageSender) Send(context.Context, message.Draft) (message.Receipt, error) {
	s.t.Error("output from canceled Submit was routed")
	return message.Receipt{}, nil
}

func TestUsageRecordedBeforeCancellationCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mail := inbox.New[message.Message]()
	a, err := New(Config{
		ID: "agent", Inbox: mail, Outbox: unexpectedUsageSender{t},
		Spec: Spec{Provider: usageProvider(func(context.Context, provider.Request) (provider.Response, error) {
			cancel()
			return provider.Response{Content: "discard", Usage: &provider.Usage{InputTokens: count(12), OutputTokens: count(3)}}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mail.Send(message.Message{Kind: message.Instruction, Content: "go"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s := a.Usage(); s.Calls != 1 || s.InputTokens != 12 || s.OutputTokens != 3 || s.Latest.ContextRevision != 2 {
		t.Fatal(s)
	}
	page, err := a.Transcript(TranscriptQuery{})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("canceled output entered history: %+v, %v", page, err)
	}
}
