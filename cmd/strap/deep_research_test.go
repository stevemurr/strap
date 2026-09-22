package main

import (
	"io"
	"testing"
)

func TestDeepResearchDefaultAndOptOut(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tt := range []struct {
		name    string
		args    []string
		enabled bool
	}{
		{"default", nil, true},
		{"disabled", []string{"-deep-research=false"}, false},
		{"web disabled", []string{"-web=false"}, false},
		{"web disabled wins", []string{"-web=false", "-deep-research=true"}, false},
		{"web disabled last", []string{"-deep-research=true", "-web=false"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseOptions(tt.args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if opts.config.DeepResearch.Enabled != tt.enabled {
				t.Fatalf("deep research enabled = %v, want %v", opts.config.DeepResearch.Enabled, tt.enabled)
			}
		})
	}
}
