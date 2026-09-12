// Strap opens an interactive terminal conversation with a local model server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/internal/tui"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/tool"
)

func run(ctx context.Context, args []string, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("strap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseURL := flags.String("base-url", "http://192.168.1.237:8355", "Local server root or API prefix")
	model := flags.String("model", "qwen3.6", "Model served by the local endpoint")
	timeout := flags.Duration("timeout", 60*time.Minute, "Timeout for each model HTTP request")
	dir := flags.String("C", ".", "Working directory for shell and file tools")
	modelOptions := modelFlags(flags)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments; run strap and type into the prompt")
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	p, err := modelOptions.newProvider(*baseURL, *model, &http.Client{Timeout: *timeout})
	if err != nil {
		return err
	}
	local, err := localTools(*dir)
	if err != nil {
		return err
	}
	c := conversation.New(ctx)
	// Tool order is what the model sees: agent.New walks Spec.Tools in order.
	messaging := []tool.Tool{tool.SendMessage(), tool.MessageStatus(c.Receipt)}
	// Auditors keep shell access with host permissions; only the writers go.
	withoutFileWrites := slices.DeleteFunc(slices.Clone(local), func(t tool.Tool) bool {
		name := t.Definition().Name
		return name == "write_file" || name == "edit_file"
	})
	executionSpec := agent.Spec{
		Provider: p,
		Prompt:   executionPrompt,
		Tools:    slices.Concat(local, messaging),
	}.Clone()
	session := workflow.New(ctx, c, executionSpec, agent.Spec{Provider: p, Prompt: auditorPrompt, Tools: slices.Concat(messaging, withoutFileWrites)})
	rootTools := slices.Concat(local, session.RootTools(), messaging, managementTools(c))
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, session.Close(cleanup))
	}()
	_, err = c.CreateAgent(message.User, agent.Spec{
		Provider: p,
		Prompt:   rootPrompt,
		Tools:    rootTools,
	})
	if err != nil {
		return err
	}
	return tui.Run(ctx, session, tui.Options{Model: *model, Endpoint: *baseURL})
}

func localTools(dir string) ([]tool.Tool, error) {
	shell, err := tool.NewShell(tool.ShellConfig{Dir: dir})
	if err != nil {
		return nil, err
	}
	files, err := tool.NewFiles(tool.FilesConfig{Dir: dir})
	if err != nil {
		return nil, err
	}
	pdf, err := tool.NewPDF(tool.PDFConfig{Dir: dir})
	if err != nil {
		return nil, err
	}
	return append([]tool.Tool{shell, pdf}, files.Tools()...), nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "strap:", err)
		os.Exit(1)
	}
}
