// Package lspconfig shares language-server flags across CLI entry points.
package lspconfig

import (
	"flag"
	"fmt"

	"github.com/stevemurr/strap/lsp"
)

type Options struct {
	enabled *bool
	path    *string
	flags   *flag.FlagSet
}

func Flags(fs *flag.FlagSet) Options {
	return Options{fs.Bool("lsp", true, "Enable experimental Go, Rust, Python, JavaScript/TypeScript and Bash language tools (servers start lazily)"), fs.String("lsp-config", "", "Language server JSON configuration; implies -lsp unless explicitly disabled"), fs}
}
func (o Options) Resolve() (*lsp.Config, error) {
	explicit := false
	o.flags.Visit(func(f *flag.Flag) {
		if f.Name == "lsp" {
			explicit = true
		}
	})
	if explicit && !*o.enabled {
		return nil, nil
	}
	if *o.path != "" {
		c, err := lsp.Load(*o.path)
		if err != nil {
			return nil, fmt.Errorf("LSP configuration: %w", err)
		}
		return &c, nil
	}
	if *o.enabled {
		c := lsp.DefaultConfig()
		return &c, nil
	}
	return nil, nil
}
