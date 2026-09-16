package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
)

type recordingSender struct {
	draft   message.Draft
	receipt message.Receipt
}

func (s *recordingSender) Send(_ context.Context, draft message.Draft) (message.Receipt, error) {
	s.draft = draft
	return s.receipt, nil
}

func TestCreateAgentCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &recordingSender{}
	invocation := Call{Actor: "agent-7", Sender: sender, Arguments: json.RawMessage(`{"role":"implementor"}`)}
	want := roster.CreateRequest{Role: roster.Implementor}
	callbackErr := errors.New("creation unavailable")
	calls := 0
	operation := CreateAgent(func(gotCtx context.Context, call Call, assignment roster.CreateRequest) (Result, error) {
		calls++
		if gotCtx != ctx || call.Actor != invocation.Actor || call.Sender != sender || string(call.Arguments) != string(invocation.Arguments) {
			t.Fatal("invocation changed at callback boundary")
		}
		if assignment != want {
			t.Fatalf("lost assignment fields: %+v", assignment)
		}
		return Text("operation result"), callbackErr
	})
	result, err := operation.Call(ctx, invocation)
	if result.Content.Text() != "operation result" || !errors.Is(err, callbackErr) || calls != 1 {
		t.Fatalf("callback outcome: %v, %v, calls=%d", result, err, calls)
	}
}

func TestCreateAgentRejectsInputBeforeCallingHandler(t *testing.T) {
	calls := 0
	operation := CreateAgent(func(context.Context, Call, roster.CreateRequest) (Result, error) {
		calls++
		return Result{}, nil
	})
	for _, raw := range []string{`{}`, `null`, `[]`, `{"task":" "}`, `{"task":"ok","instructions":"override"}`, `{"task":"ok","actor":"forged"}`, `{"task":"ok"} {}`} {
		if _, err := operation.Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := operation.Call(ctx, Call{Arguments: json.RawMessage(`{"role":"implementor"}`)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call: %v", err)
	}
	if calls != 0 {
		t.Fatalf("called handler %d times", calls)
	}
}

func TestMessageStatusLookup(t *testing.T) {
	want := message.Receipt{MessageID: "message-3", Recipient: "agent-2", Status: message.Consumed}
	calls := 0
	operation := MessageStatus(func(id message.MessageID) (message.Receipt, bool) {
		calls++
		return want, id == want.MessageID
	})
	result, err := operation.Call(context.Background(), Call{Arguments: json.RawMessage(`{"message_id":"message-3"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var got message.Receipt
	if err := json.Unmarshal([]byte(result.Content.Text()), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("receipt changed: %+v", got)
	}
	if _, err := operation.Call(context.Background(), Call{Arguments: json.RawMessage(`{"message_id":"unknown"}`)}); err == nil {
		t.Fatal("unknown receipt accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := operation.Call(ctx, Call{Arguments: json.RawMessage(`{"message_id":"message-3"}`)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup: %v", err)
	}
	if calls != 2 {
		t.Fatalf("lookup calls: %d", calls)
	}
}

func TestSendMessageUsesInvocationSender(t *testing.T) {
	want := message.Receipt{MessageID: "message-1", Recipient: "agent-2", Status: message.Queued}
	sender := &recordingSender{receipt: want}
	result, err := SendMessage().Call(context.Background(), Call{
		Actor: "agent-1", Sender: sender,
		Arguments: json.RawMessage(`{"to":"agent-2","message":"follow up"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if sender.draft.To != "agent-2" || sender.draft.Kind != message.Instruction || sender.draft.Content != "follow up" {
		t.Fatalf("wrong draft: %+v", sender.draft)
	}
	var got message.Receipt
	if err := json.Unmarshal([]byte(result.Content.Text()), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("receipt changed: %+v", got)
	}
}

func TestManagementCallbacks(t *testing.T) {
	constructors := []func(func(context.Context, Call, message.ActorID) (Result, error)) Tool{StopAgent, PauseAgent, ResumeAgent}
	for _, constructor := range constructors {
		calls := 0
		operation := constructor(func(_ context.Context, call Call, id message.ActorID) (Result, error) {
			calls++
			if call.Actor != "root" || id != "agent-2" {
				t.Fatal("lost caller or target")
			}
			return Text("acknowledged"), nil
		})
		result, err := operation.Call(context.Background(), Call{Actor: "root", Arguments: json.RawMessage(`{"agent_id":"agent-2"}`)})
		if err != nil || result.Content.Text() != "acknowledged" {
			t.Fatalf("management call: %+v %v", result, err)
		}
		for _, raw := range []string{`{}`, `{"agent_id":" "}`, `{"agent_id":"agent-2","actor":"forged"}`, `{"agent_id":"a","agent_id":"b"}`} {
			if _, err := operation.Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		}
		if calls != 1 {
			t.Fatal("invalid input reached handler")
		}
	}
	calls := 0
	list := ListAgents(func(context.Context, Call) (Result, error) { calls++; return JSON([]string{"root"}) })
	if _, err := list.Call(context.Background(), Call{Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := list.Call(context.Background(), Call{Arguments: json.RawMessage(`{"unused":true}`)}); err == nil {
		t.Fatal("accepted unexpected list arguments")
	}
	if calls != 1 {
		t.Fatal("invalid list input reached handler")
	}
}

func TestInspectAgentContract(t *testing.T) {
	calls := 0
	operation := InspectAgent(func(_ context.Context, c Call, args InspectAgentArgs) (Result, error) {
		calls++
		if c.Actor != "root" || args.AgentID != "agent-2" {
			t.Fatal("lost caller/target")
		}
		return JSON(args)
	})
	for _, raw := range []string{`{"agent_id":"agent-2"}`, `{"agent_id":"agent-2","limit":100,"before":21}`} {
		if _, err := operation.Call(context.Background(), Call{Actor: "root", Arguments: json.RawMessage(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{}`, `{"agent_id":" "}`, `{"agent_id":"agent-2","limit":0}`, `{"agent_id":"agent-2","limit":101}`, `{"agent_id":"agent-2","before":0}`, `{"agent_id":"agent-2","before":-1}`, `{"agent_id":"agent-2","actor":"forged"}`} {
		if _, err := operation.Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if calls != 2 {
		t.Fatal("invalid input reached callback")
	}
}
