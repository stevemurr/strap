package livetest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stevemurr/strap/harness/machine"
)

// scenarios names each live scenario's user messages in the order it sends
// them, so a recorded trace can be checked without the endpoint.
var scenarios = map[string][]machine.Intent{
	"lifecycle": {machine.Task},
	"update":    {machine.Task, machine.Update},
	"clarify":   {machine.Task, machine.Ambiguous, machine.Answer},
	"machine":   {machine.Chat, machine.Task, machine.Question, machine.FollowUp},
}

// Recorded scenarios replay through the machine without the endpoint:
//
//	STRAP_LIVE_REPLAY=<output dir of an earlier run> go test ./harness/livetest -run TestMachineReplay -v
func TestMachineReplay(t *testing.T) {
	dir := os.Getenv("STRAP_LIVE_REPLAY")
	if dir == "" {
		t.Skip("set STRAP_LIVE_REPLAY to a live-test output directory")
	}
	found := false
	for name, intents := range scenarios {
		path := filepath.Join(dir, name, "trace.jsonl")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		found = true
		t.Run(name, func(t *testing.T) {
			tl, err := machine.Load(context.Background(), path, intents)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("paths:\n%s", tl.Paths())
			for _, v := range tl.Violations(true) {
				t.Error(v)
			}
		})
	}
	if !found {
		t.Fatalf("no scenario traces under %s", dir)
	}
}
