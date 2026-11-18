// Package scaffold writes the starter configuration files into a folder.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/letkode/db-anon-dump/templates"
)

// Write creates config.yaml, columns.yaml and functions.yaml in dir (created if needed) from the embedded
// templates. Without force it refuses to touch anything if any of the files already exists, so a partial
// scaffold is never left behind. It returns the files it wrote.
func Write(dir string, force bool) ([]string, error) {
	if !force {
		var existing []string
		for _, name := range templates.Files {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				existing = append(existing, name)
			}
		}
		if len(existing) > 0 {
			return nil, fmt.Errorf("%v already exist in %s (use --force to overwrite)", existing, dir)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, name := range templates.Files {
		body, err := templates.FS.ReadFile(name + ".dist")
		if err != nil {
			return written, err
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}
