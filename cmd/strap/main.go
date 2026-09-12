// Strap opens an interactive terminal conversation with a local model server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/tui"
	"github.com/stevemurr/strap/tool"
)

func run(ctx context.Context, args []string, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("strap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseURL := flags.String("base-url", "http://192.168.1.237:8355", "Local server root or API prefix")
	model := flags.String("model", "qwen3.6", "Model served by the local endpoint")
	timeout := flags.Duration("timeout", 60*time.Minute, "Timeout for each model HTTP request")
	dir := flags.String("C", ".", "Working directory for shell and file tools")
	webEnabled := flags.Bool("web", true, "Enable web_search and open_url (backends start lazily)")
	wkPath := flags.String("wkrender", "", "Path to wkrender (default PATH or ~/.harness/bin/wkrender)")
	abPath := flags.String("agent-browser", "", "Path to agent-browser 0.37.1 (default PATH or Strap's isolated installation)")
	browserPath := flags.String("browser-executable", "", "Chrome executable for open_url (default installed Chrome on macOS or agent-browser discovery)")
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
	cfg := harness.DefaultConfig()
	cfg.Dir = *dir
	cfg.Model = modelOptions.config(*baseURL, *model)
	cfg.Model.Timeout = *timeout
	cfg.Web = nil
	if *webEnabled {
		cfg.Web = &tool.WebConfig{WKRenderPath: *wkPath, AgentBrowserPath: *abPath, BrowserExecutablePath: *browserPath}
	}
	session, err := harness.New(ctx, cfg, harness.Dependencies{})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = errors.Join(err, session.Close(cleanup))
	}()
	return tui.Run(ctx, session, tui.Options{Model: *model, Endpoint: *baseURL})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "strap:", err)
		os.Exit(1)
	}
}
