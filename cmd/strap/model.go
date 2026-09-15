package main

import (
	"flag"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/modelcatalog"
)

func modelFlags(flags *flag.FlagSet, model *harness.ModelConfig) { modelcatalog.Flags(flags, model) }
