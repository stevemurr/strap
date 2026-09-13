package inspection_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
)

// The user-supplied production archive stays outside source control. This opt-in
// acceptance check verifies known evidence through the public inspection API.
func TestLengthFailureArchive(t *testing.T) {
	path := os.Getenv("STRAP_LENGTH_TRACE")
	if path == "" {
		t.Skip("set STRAP_LENGTH_TRACE to trace-20260912-175308.jsonl")
	}
	ctx := context.Background()
	r, err := inspection.OpenJSONL(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(ctx)
	head, err := r.Head(ctx)
	if err != nil || head.Cursor.Sequence != 40329 || head.State != eventlog.Sealed {
		t.Fatal(head, err)
	}
	v, err := r.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	id := identity.OutputID{Agent: "agent-4", Call: 6}
	out, err := v.InspectOutput(ctx, id)
	if err != nil || out.Status != agent.OutputFailed || out.TextBytes != 0 || out.ReasoningBytes != 500744 || out.HistoryPosition != nil || out.Error == nil || out.Error.Message != `vllm: incomplete or unsupported finish reason "length"` {
		t.Fatalf("failure evidence: %+v %v", out, err)
	}
	p, err := v.ReadOutputText(ctx, inspection.OutputTextQuery{Output: id, Channel: provider.ChannelReasoning, MaxBytes: 1 << 20})
	if err != nil || !p.End {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(p.Text))
	if hex.EncodeToString(sum[:]) != "36e189056858239f8d088988f7835053cc6304676ba101d88b84277fb7235c49" {
		t.Fatal("retained reasoning changed")
	}
}
