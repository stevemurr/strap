package main

import (
	"context"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellExample(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	old := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = old }()
	main()
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hello from stdout", "status 7", "truncated", "The call timed out"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
}
func TestShellExampleFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx); err == nil {
		t.Fatal("canceled example succeeded")
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if err := run(context.Background()); err == nil {
		t.Fatal("invalid temporary directory accepted")
	}
}

type shellOperation struct{}

func (shellOperation) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "fixture"}
}
func (shellOperation) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Text("invalid JSON"), nil
}
func TestShellExampleRejectsMalformedResult(t *testing.T) {
	if err := runSteps(context.Background(), shellOperation{}); err == nil {
		t.Fatal("malformed result accepted")
	}
}
func TestShellExampleMainFailureExit(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	code := 0
	mainWithExit(func(got int) { code = got })
	if code != 1 {
		t.Fatal(code)
	}
}
