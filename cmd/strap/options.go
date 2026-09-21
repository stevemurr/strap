package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/modelflags"
)

type options struct {
	config harness.Config
	listen string
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	o := options{config: harness.DefaultConfig()}
	flags := flag.NewFlagSet("strap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, "usage:\n  strap [flags]\n  strap eval <command> [options]  (see strap eval -help)\n\nflags:\n")
		flags.PrintDefaults()
	}
	model := modelflags.Register(flags, &o.config, "timeout")
	flags.StringVar(&o.config.Dir, "C", o.config.Dir, "Working directory for shell and file tools")
	flags.StringVar(&o.config.Events.JSONLPath, "record", "", "Record session events and tool diagnostics to a new JSONL file")
	flags.StringVar(&o.listen, "listen", "", "Serve the harness HTTP API at a loopback address (requires STRAP_API_TOKEN)")
	webEnabled := flags.Bool("web", o.config.Web != nil, "Enable web_search and open_url (backends start lazily)")
	flags.StringVar(&o.config.Web.WKRenderPath, "wkrender", "", "Path to wkrender (default PATH or ~/.harness/bin/wkrender)")
	flags.StringVar(&o.config.Web.AgentBrowserPath, "agent-browser", "", "Path to agent-browser (default PATH or Strap's isolated installation)")
	flags.StringVar(&o.config.Web.BrowserExecutablePath, "browser-executable", "", "Chrome executable for open_url (default installed Chrome on macOS or agent-browser discovery)")
	// Discover the catalog/profile with the real parser so help and syntax errors
	// work before reading files. Reparse after loading to give every flag precedence.
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("unexpected arguments; run strap and type into the prompt")
	}
	// Provider validation happens before either terminal startup or opening
	// the HTTP listener.
	if _, err := model.Model(args); err != nil {
		return options{}, err
	}
	info, err := os.Stat(o.config.Dir)
	if err != nil {
		return options{}, fmt.Errorf("working directory: %w", err)
	}
	if !info.IsDir() {
		return options{}, fmt.Errorf("working directory %q is not a directory", o.config.Dir)
	}
	if !*webEnabled {
		o.config.Web = nil
	}
	if err := model.Languages(); err != nil {
		return options{}, err
	}
	return o, nil
}
