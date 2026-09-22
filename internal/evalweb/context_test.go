package evalweb

import (
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"testing"
)

func TestAgentContextSnapshot(t *testing.T) {
	task := &TaskState{ID: "problem"}
	j := &job{tasks: []*TaskState{task}, byID: map[string]*TaskState{"problem": task}, changed: make(chan struct{})}
	observe := func(revision uint64, count int64, err string) {
		j.observe(eval.Progress{Task: eval.Task{ID: "problem"}, Event: conversation.ContextTokensEvent{Agent: "worker", Revision: revision, Count: count, Error: err}})
	}
	observe(4, 4096, "")
	snapshot := j.snapshot()
	observe(3, 100, "")
	if task.Context["worker"].Tokens != 4096 {
		t.Fatal("older revision replaced latest context")
	}
	observe(5, 8192, "")
	if snapshot.Tasks[0].Context["worker"].Tokens != 4096 {
		t.Fatal("snapshot mutated")
	}
	observe(6, 0, "unavailable")
	if j.snapshot().Tasks[0].Context["worker"].Error != "unavailable" {
		t.Fatal("lost measurement error")
	}
	if j.events[0].Kind != "context_tokens" || j.events[0].Agent != "worker" {
		t.Fatal("lost context event")
	}
}
