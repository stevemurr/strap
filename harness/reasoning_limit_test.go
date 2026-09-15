package harness_test

import (
	"context"
	"testing"

	"github.com/stevemurr/strap/harness"
)

func TestReasoningLimitDefaultsAndValidation(t *testing.T) {
	cfg := harness.DefaultConfig()
	if cfg.ReasoningLimit != 192<<10 {
		t.Fatal(cfg.ReasoningLimit)
	}
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	cfg.ReasoningLimit = -1
	if _, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: &waitScript{}}); err == nil {
		t.Fatal("negative limit accepted")
	}
	cfg.ReasoningLimit = 64 << 10
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: &waitScript{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if got := s.Inspect().Config.ReasoningLimit; got != 64<<10 {
		t.Fatal(got)
	}
}
