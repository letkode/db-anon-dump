// Package envfile loads layered .env files into the process environment.
package envfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// Files returns the files Apply reads for appEnv, in the order they are merged (later ones win):
//
//	.env  <  .env.local  <  .env.<appEnv>  <  .env.<appEnv>.local
//
// The two appEnv files are only included when appEnv is not empty.
func Files(dir, appEnv string) []string {
	files := []string{filepath.Join(dir, ".env"), filepath.Join(dir, ".env.local")}
	if appEnv != "" {
		base := filepath.Join(dir, ".env."+appEnv)
		files = append(files, base, base+".local")
	}
	return files
}

// Apply merges the .env files of dir into the process environment and returns the names it set.
//
// The environment is chosen by the APP_ENV variable: the real environment first, otherwise the value found in
// .env / .env.local. Without APP_ENV only .env and .env.local are read. A missing file is skipped; a file that
// cannot be parsed is an error. A variable already set in the real environment is NEVER overridden.
func Apply(dir string) ([]string, error) {
	vars := map[string]string{}
	read := func(files []string) error {
		for _, f := range files {
			m, err := godotenv.Read(f)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			for k, v := range m {
				vars[k] = v
			}
		}
		return nil
	}
	if err := read(Files(dir, "")); err != nil {
		return nil, err
	}
	appEnv, ok := os.LookupEnv("APP_ENV")
	if !ok {
		appEnv = vars["APP_ENV"]
	}
	if appEnv = strings.ToLower(strings.TrimSpace(appEnv)); appEnv != "" {
		if err := read(Files(dir, appEnv)[2:]); err != nil {
			return nil, err
		}
	}
	var set []string
	for k, v := range vars {
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return set, err
		}
		set = append(set, k)
	}
	return set, nil
}
