package eventcodec_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/provider"
)

func TestRejectedArgumentsSurviveFailureRecordRoundTrip(t *testing.T) {
	for _, raw := range []string{"", " \t{\"message\":\"hé\\nllo\"\n", "[]", "null"} {
		t.Run(raw, func(t *testing.T) {
			want := provider.ToolArgumentsError{CallID: "c1", Name: "send_message", Arguments: raw}
			cause := errors.Join(fmt.Errorf("vllm: %w", &want), errors.New("another failure"))
			fact := conversation.AgentEvent{Agent: "agent", Event: agent.OutputFinished{Output: codecOutput, Status: agent.OutputFailed, Err: cause}}
			d, err := eventcodec.EncodeEvent(fact)
			if err != nil {
				t.Fatal(err)
			}
			var wire record.OutputFinished
			if err := json.Unmarshal(d.Payload, &wire); err != nil || wire.RejectedToolCall == nil || *wire.RejectedToolCall != want {
				t.Fatalf("invalid failure record: %s, %v", d.Payload, err)
			}
			if wire.Error == nil || wire.Error.Code != "generation_failed" || wire.Error.Message != cause.Error() {
				t.Fatalf("changed error classification/message: %+v", wire.Error)
			}
			for i := 0; i < 2; i++ {
				fact = roundTrip(t, fact).(conversation.AgentEvent)
				failure := fact.Event.(agent.OutputFinished)
				var detail *provider.ToolArgumentsError
				if !errors.As(failure.Err, &detail) || *detail != want || failure.Err.Error() != cause.Error() {
					t.Fatalf("lost error evidence: %+v, %v", detail, failure.Err)
				}
			}
		})
	}
}
