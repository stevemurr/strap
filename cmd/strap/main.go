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
)

func run(ctx context.Context, args []string, stderr io.Writer) (err error) {
	opts, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg := opts.config
	if opts.listen != "" {
		return runHTTP(ctx, cfg, opts.listen, os.Getenv("STRAP_API_TOKEN"))
	}
	session, err := harness.New(ctx, cfg, harness.Dependencies{})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = errors.Join(err, session.Dispose(cleanup))
	}()
	return tui.Run(ctx, session, tui.Options{Dir: cfg.Dir, Model: cfg.Model.Model, Endpoint: cfg.Model.BaseURL})
}

// version is stamped by the release build; a source build reports "dev" so a
// bug report can say which binary produced it.
var version = "dev"

func main() {
	for _, a := range os.Args[1:] {
		if a == "-version" || a == "--version" {
			fmt.Println("strap " + version)
			return
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "strap:", err)
		os.Exit(1)
	}
}
