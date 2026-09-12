package conversation_test

import (
	"context"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

type limitedCountingProvider struct {
	*countingProvider
	limit int64
}

func (p *limitedCountingProvider) OutputTokenLimit() *int64 { return &p.limit }

func TestInspectionExposesPerAgentLimitsAndCountsAfterOrdinaryReplies(t *testing.T) {
	c := conversation.New(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	rootProvider := &limitedCountingProvider{countingProvider: &countingProvider{controlledProvider: &controlledProvider{calls: make(chan call, 16)}}, limit: 32768}
	childProvider := &limitedCountingProvider{countingProvider: &countingProvider{controlledProvider: &controlledProvider{calls: make(chan call, 16)}}, limit: 8192}
	root, err := c.CreateAgent(message.User, agent.Spec{Provider: rootProvider})
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.CreateAgent(root.AgentID, agent.Spec{Provider: childProvider})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id    message.ActorID
		limit int64
	}{{root.AgentID, 32768}, {child.AgentID, 8192}} {
		in, err := c.InspectAgent(tc.id, conversation.InspectOptions{})
		if err != nil || in.OutputTokenLimit == nil || *in.OutputTokenLimit != tc.limit || in.ContextRevision != 1 {
			t.Fatalf("inspection: %+v %v", in, err)
		}
		*in.OutputTokenLimit = 1
		again, _ := c.InspectAgent(tc.id, conversation.InspectOptions{})
		if *again.OutputTokenLimit != tc.limit {
			t.Fatal("inspection aliases provider")
		}
	}
	for i, text := range []string{"hello", "follow-up"} {
		if _, err := c.Send(root.AgentID, text); err != nil {
			t.Fatal(err)
		}
		rootProvider.next(t).answer <- answer{response: provider.Response{Content: text + " reply", Usage: usage(100, 20)}}
		userReply(t, c, text+" reply")
		in, err := c.InspectAgent(root.AgentID, conversation.InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if in.ContextRevision != uint64(3+i*2) || in.Usage.OutputTokens != int64(20*(i+1)) || *in.Usage.Latest.Usage.OutputTokens != 20 || *in.OutputTokenLimit != 32768 {
			t.Fatalf("incorrect call/context accounting: %+v", in)
		}
		rootProvider.count = func(_ context.Context, r provider.Request) (int64, error) {
			if r.Agent != root.AgentID || uint64(len(r.Messages)) != in.ContextRevision || r.Messages[len(r.Messages)-1].Content[0].Text != text+" reply" {
				t.Fatalf("missing latest reply: %+v", r)
			}
			return 12345, nil
		}
		if n, err := c.CountAgentTokens(context.Background(), root.AgentID, in.ContextRevision); err != nil || n != 12345 {
			t.Fatalf("count: %d %v", n, err)
		}
	}
}
