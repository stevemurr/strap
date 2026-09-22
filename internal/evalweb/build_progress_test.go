package evalweb

import (
	"context"
	"github.com/stevemurr/strap/eval"
	"strings"
	"testing"
)

func TestBuildProgressWriter(t *testing.T) {
	var events []JobEvent
	w := &buildProgressWriter{report: func(e JobEvent) { events = append(events, e) }}
	w.Write([]byte("Step 1: copy"))
	w.Write([]byte(" files\r\nStep 2: build\nDone"))
	w.flush()
	if len(events) != 3 || events[0].Text != "Step 1: copy files" || events[2].Text != "Done" {
		t.Fatalf("unexpected progress: %+v", events)
	}
	for _, e := range events {
		if e.Kind != "build" || e.Phase != "building" {
			t.Fatalf("unexpected event: %+v", e)
		}
	}
	w.Write([]byte(strings.Repeat("x", 10000)))
	if len(w.pending) >= 4096 {
		t.Fatal("unbounded partial line")
	}
}

func TestContainerPreparationProgress(t *testing.T) {
	for _, mode := range []string{"success", "build-fail", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := fakeContainer(t, mode)
			r.NoBuild = mode == "reuse"
			tasks, err := eval.LoadProblems(r.Ladder)
			if err != nil {
				t.Fatal(err)
			}
			var events []JobEvent
			_, err = r.prepareContainers(context.Background(), t.TempDir(), tasks[:1], func(e JobEvent) { events = append(events, e) })
			if (err != nil) != (mode == "build-fail") {
				t.Fatalf("preparation error: %v", err)
			}
			want := []string{"preparing", "building", "ready"}
			if mode == "reuse" {
				want = []string{"preparing", "ready"}
			}
			if mode == "build-fail" {
				want = []string{"preparing", "building"}
			}
			if len(events) != len(want) {
				t.Fatalf("unexpected events: %+v", events)
			}
			for i, phase := range want {
				if events[i].Kind != "build" || events[i].Phase != phase {
					t.Fatalf("event %d: %+v", i, events[i])
				}
			}
		})
	}
}
