package evalweb

import (
	"github.com/stevemurr/strap/eval"
	"testing"
)

func TestDefaultEvalName(t *testing.T) {
	for _, tc := range []struct {
		tasks []eval.Task
		want  string
	}{
		{[]eval.Task{{ID: "one", Title: "Budget pair", Tier: "easy"}}, "Qwen · Budget pair"},
		{[]eval.Task{{ID: "one", Tier: "easy"}}, "Qwen · one"},
		{[]eval.Task{{Tier: "easy"}, {Tier: "easy"}}, "Qwen · 2 easy problems"},
		{[]eval.Task{{Tier: "easy"}, {Tier: "hard"}}, "Qwen · 2 problems"},
	} {
		if got := defaultEvalName("Qwen", tc.tasks); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}
