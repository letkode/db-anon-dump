// Package templates embeds the commented *.dist files that `dbanondump init` writes.
package templates

import "embed"

// FS holds the three templates.
//
//go:embed config.yaml.dist columns.yaml.dist functions.yaml.dist
var FS embed.FS

// Files are the names written by `init` (the template name without the ".dist" suffix).
var Files = []string{"config.yaml", "columns.yaml", "functions.yaml"}
