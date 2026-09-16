package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider/vllm"
)

type options struct {
	config harness.Config
	listen string
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	o := options{config: harness.DefaultConfig()}
	// The CLI's model defaults live in models.json, independently of library defaults.
	o.config.Model = harness.ModelConfig{Backend: "vllm", Timeout: o.config.Model.Timeout}
	flags := flag.NewFlagSet("strap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "Model catalog JSON (default $XDG_CONFIG_HOME/strap/models.json or ~/.config/strap/models.json, then bundled catalog)")
	profile := flags.String("profile", "", "Saved model profile (default selected by the catalog)")
	modelFlags(flags, &o.config.Model)
	flags.StringVar(&o.config.Dir, "C", o.config.Dir, "Working directory for shell and file tools")
	flags.IntVar(&o.config.ReasoningLimit, "reasoning-limit", o.config.ReasoningLimit, "Reasoning bytes a model call may stream before it is cut off and retried once (0 disables)")
	flags.StringVar(&o.config.Events.JSONLPath, "record", "", "Record session events and tool diagnostics to a new JSONL file")
	flags.StringVar(&o.listen, "listen", "", "Serve the harness HTTP API at a loopback address (requires STRAP_API_TOKEN)")
	webEnabled := flags.Bool("web", o.config.Web != nil, "Enable web_search and open_url (backends start lazily)")
	flags.StringVar(&o.config.Web.WKRenderPath, "wkrender", "", "Path to wkrender (default PATH or ~/.harness/bin/wkrender)")
	flags.StringVar(&o.config.Web.AgentBrowserPath, "agent-browser", "", "Path to agent-browser 0.37.1 (default PATH or Strap's isolated installation)")
	flags.StringVar(&o.config.Web.BrowserExecutablePath, "browser-executable", "", "Chrome executable for open_url (default installed Chrome on macOS or agent-browser discovery)")
	// Discover the catalog/profile with the real parser so help and syntax errors
	// work before reading files. Reparse after loading to give every flag precedence.
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("unexpected arguments; run strap and type into the prompt")
	}
	model, err := loadModel(*configPath, *profile, o.config.Model.Timeout)
	if err != nil {
		return options{}, err
	}
	if flagWasSet(flags, "backend") && o.config.Model.Backend == "chatcompletions" {
		// The generic backend uses server defaults unless flags explicitly override.
		model.Generation = vllm.Generation{}
	}
	o.config.Model = model
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if o.config.Model.Timeout <= 0 {
		return options{}, errors.New("timeout must be positive")
	}
	// Provider construction validates options without connecting to the server.
	// Do this before either terminal startup or opening the HTTP listener.
	if _, err := o.config.Model.NewProvider(nil); err != nil {
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
	return o, nil
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
