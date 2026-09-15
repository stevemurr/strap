package main

import "path/filepath"

// catalogPath is the bundled catalog, which moved to internal/modelcatalog so
// strap-eval shares it.
var catalogPath = filepath.Join("..", "..", "internal", "modelcatalog", "models.json")
