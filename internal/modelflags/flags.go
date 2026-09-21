// Package modelflags registers the model, catalog, language-server and
// reasoning flags shared by every command that starts a harness session, and
// resolves them in the order the CLI documents: discover the catalog and
// profile, load it, then let every explicit flag override the profile.
package modelflags

import (
	"errors"
	"flag"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/lspconfig"
	"github.com/stevemurr/strap/internal/modelcatalog"
)

// Options holds the registered flags for one command until Parse has run.
type Options struct {
	flags     *flag.FlagSet
	config    *harness.Config
	catalog   string
	profile   string
	languages lspconfig.Options
}

// Register adds the shared flags to fs and points them at cfg. The CLI's model
// defaults live in the catalog, independently of library defaults, so cfg.Model
// is reset to the vllm backend with only its timeout kept. modelTimeout names
// the model request timeout flag; commands that reserve -timeout for another
// budget pass a different name.
func Register(fs *flag.FlagSet, cfg *harness.Config, modelTimeout string) *Options {
	cfg.Model = harness.ModelConfig{Backend: "vllm", Timeout: cfg.Model.Timeout}
	o := &Options{flags: fs, config: cfg, languages: lspconfig.Flags(fs)}
	fs.StringVar(&o.catalog, "config", "", "Model catalog JSON (default $XDG_CONFIG_HOME/strap/models.json or ~/.config/strap/models.json, then bundled catalog)")
	fs.StringVar(&o.profile, "profile", "", "Saved model profile (default selected by the catalog)")
	if modelTimeout == "timeout" {
		modelcatalog.Flags(fs, &cfg.Model)
	} else {
		renamed := flag.NewFlagSet("model", flag.ContinueOnError)
		modelcatalog.Flags(renamed, &cfg.Model)
		renamed.VisitAll(func(f *flag.Flag) {
			name := f.Name
			if name == "timeout" {
				name = modelTimeout
			}
			fs.Var(f.Value, name, f.Usage)
		})
	}
	fs.IntVar(&cfg.ReasoningLimit, "reasoning-limit", cfg.ReasoningLimit, "Reasoning bytes a model call may stream before it is cut off and retried once (0 disables)")
	return o
}

// Model loads the selected profile into the config, reparses args so explicit
// flags win, and validates the provider without connecting. It returns the
// profile name. Call it after the first Parse.
func (o *Options) Model(args []string) (string, error) {
	name, err := modelcatalog.Apply(o.flags, args, &o.config.Model, o.catalog, o.profile)
	if err != nil {
		return "", err
	}
	if o.config.Model.Timeout <= 0 {
		return "", errors.New("timeout must be positive")
	}
	if _, err := o.config.Model.NewProvider(nil); err != nil {
		return "", err
	}
	return name, nil
}

// Languages resolves the language-server flags into the config.
func (o *Options) Languages() error {
	languages, err := o.languages.Resolve()
	if err != nil {
		return err
	}
	o.config.LSP = languages
	return nil
}
