// Demonstrate file tools directly, using a temporary project directory.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/stevemurr/strap/tool"
)

func run(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "strap-files-example-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	files, err := tool.NewFiles(tool.FilesConfig{Dir: dir})
	if err != nil {
		return err
	}
	// The adapters share one Files instance and are selected by their tool names.
	kit := make(map[string]tool.Tool)
	for _, operation := range files.Tools() {
		kit[operation.Definition().Name] = operation
	}
	return runSteps(ctx, kit)
}

func runSteps(ctx context.Context, kit map[string]tool.Tool) error {
	steps := []struct {
		label     string
		name      string
		args      json.RawMessage
		wantError bool
	}{
		{
			label: "Write literal text, including newlines", name: "write_file",
			args: json.RawMessage(`{"input":{"path":"tasks.txt","content":"Task: document tools\nStatus: draft\nTask: run examples\nStatus: draft\n"}}`),
		},
		{
			label: "Read a window starting at line 3", name: "read_file",
			args: json.RawMessage(`{"input":{"path":"tasks.txt","offset":3,"limit":2}}`),
		},
		{
			label: "An ambiguous edit returns an error and leaves the file unchanged", name: "edit_file",
			args: json.RawMessage(`{"input":{"path":"tasks.txt","old":"Status: draft","new":"Status: done"}}`), wantError: true,
		},
		{
			label: "Retry with enough surrounding text to identify one match", name: "edit_file",
			args: json.RawMessage(`{"input":{"path":"tasks.txt","old":"Task: run examples\nStatus: draft","new":"Task: run examples\nStatus: done"}}`),
		},
		{
			label: "Read the updated file", name: "read_file",
			args: json.RawMessage(`{"input":{"path":"tasks.txt","offset":null,"limit":null}}`),
		},
	}
	for _, step := range steps {
		fmt.Printf("\n%s\n%s %s\n", step.label, step.name, step.args)
		output, err := kit[step.name].Call(ctx, tool.Call{Arguments: step.args})
		if step.wantError {
			if err == nil {
				return fmt.Errorf("expected an ambiguous-edit error")
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Printf("Expected error: %v\n", err)
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		fmt.Println(output.Content.Text())
		if step.name == "read_file" {
			// ReadFileResult exposes the numbered contents for a host to display.
			var result tool.ReadFileResult
			if err := json.Unmarshal([]byte(output.Content.Text()), &result); err != nil {
				return err
			}
			fmt.Print(result.Content)
		}
	}
	return nil
}

func main() { mainWithExit(os.Exit) }

func mainWithExit(exit func(int)) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
}
