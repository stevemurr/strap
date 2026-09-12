package main

import (
	"context"
	"errors"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileExample(t *testing.T) {
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
	for _, want := range []string{"Expected error:", "Status: done", "3\tTask: run examples"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
}
func TestFileExampleFailures(t *testing.T) {
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

type fileOperation func(context.Context, tool.Call) (tool.Result, error)

func (f fileOperation) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "fixture"}
}
func (f fileOperation) Call(ctx context.Context, c tool.Call) (tool.Result, error) { return f(ctx, c) }
func TestFileExampleReportsOperationFailures(t *testing.T) {
	for _, mode := range []string{"unexpected edit success", "invalid read result", "canceled edit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			kit := map[string]tool.Tool{
				"write_file": fileOperation(func(context.Context, tool.Call) (tool.Result, error) { return tool.Text(`{}`), nil }),
				"read_file": fileOperation(func(context.Context, tool.Call) (tool.Result, error) {
					if mode == "invalid read result" {
						return tool.Text("invalid JSON"), nil
					}
					return tool.Text(`{}`), nil
				}),
				"edit_file": fileOperation(func(context.Context, tool.Call) (tool.Result, error) {
					if mode == "canceled edit" {
						cancel()
						return tool.Result{}, context.Canceled
					}
					return tool.Text(`{}`), nil
				}),
			}
			err := runSteps(ctx, kit)
			if err == nil {
				t.Fatal("expected operation failure")
			}
			if mode == "canceled edit" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if mode == "unexpected edit success" && !strings.Contains(err.Error(), "expected an ambiguous-edit error") {
				t.Fatal(err)
			}
		})
	}
}
func TestFileExampleMainFailureExit(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	code := 0
	mainWithExit(func(got int) { code = got })
	if code != 1 {
		t.Fatal(code)
	}
}
