// Demonstrate the shell tool directly, without a model or credentials.
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
	dir, err := os.MkdirTemp("", "strap-shell-example-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	shell, err := tool.NewShell(tool.ShellConfig{
		Dir: dir, Timeout: 2 * time.Second, MaxTimeout: 3 * time.Second,
		OutputLimit: 32, // Small enough to demonstrate truncation below.
	})
	if err != nil {
		return err
	}

	return runSteps(ctx, shell)
}

func runSteps(ctx context.Context, shell tool.Tool) error {
	// These are the same JSON arguments an agent passes to Tool.Call.
	steps := []struct {
		label string
		args  json.RawMessage
	}{
		{"Run a command and combine stdout/stderr", json.RawMessage(`{"command":"printf 'hello from stdout\n'; printf 'and stderr\n' >&2"}`)},
		{"Inspect a nonzero exit (a normal tool result)", json.RawMessage(`{"command":"printf 'check failed\n'; exit 7"}`)},
		{"Keep both ends of long output", json.RawMessage(`{"command":"printf 'START-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-END\n'"}`)},
		{"Override the timeout for one call", json.RawMessage(`{"command":"printf 'started\n'; sleep 2","timeout_ms":100}`)},
	}
	for _, step := range steps {
		fmt.Printf("\n%s\nshell %s\n", step.label, step.args)
		output, err := shell.Call(ctx, tool.Call{Arguments: step.args})
		if err != nil {
			return fmt.Errorf("%s: %w", step.label, err)
		}
		fmt.Println(output.Content.Text())

		// Hosts can decode the JSON instead of parsing human-readable output.
		var result tool.ShellResult
		if err := json.Unmarshal([]byte(output.Content.Text()), &result); err != nil {
			return err
		}
		if result.TimedOut {
			fmt.Println("The call timed out; captured output is still available.")
		} else if result.ExitCode != nil && *result.ExitCode != 0 {
			fmt.Printf("The command ran and exited with status %d.\n", *result.ExitCode)
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
