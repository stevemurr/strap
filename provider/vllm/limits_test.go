package vllm_test

import (
	"testing"

	"github.com/stevemurr/strap/provider/vllm"
)

func TestOutputTokenLimitOwnsConfiguredSnapshot(t *testing.T) {
	limit := 8192
	p, err := vllm.New(vllm.Config{BaseURL: "http://localhost:1", Model: "local", Generation: vllm.Generation{MaxTokens: &limit}})
	if err != nil {
		t.Fatal(err)
	}
	limit = 100
	if got := p.OutputTokenLimit(); got == nil || *got != 8192 {
		t.Fatal(got)
	}
	*p.OutputTokenLimit() = 1
	if *p.OutputTokenLimit() != 8192 {
		t.Fatal("caller changed provider cap")
	}
	defaults, err := vllm.New(vllm.Config{BaseURL: "http://localhost:1", Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.OutputTokenLimit() != nil {
		t.Fatal("invented server default")
	}
}
