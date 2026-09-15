package main

import (
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/modelcatalog"
)

// loadModel resolves a saved profile; the catalog and its bundled fallback live
// in internal/modelcatalog so strap-eval selects models the same way.
func loadModel(path, profile string, timeout time.Duration) (harness.ModelConfig, error) {
	return modelcatalog.Load(path, profile, timeout)
}
