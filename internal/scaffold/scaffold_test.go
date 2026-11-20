package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letkode/db-anon-dump/internal/config"
	"github.com/letkode/db-anon-dump/internal/engine"
)

func TestWriteCreatesTheFilesAndTheyAreValid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "folder") // does not exist yet
	written, err := Write(dir, false)
	if err != nil || len(written) != 4 {
		t.Fatalf("Write: %v %v", written, err)
	}
	for _, n := range []string{"config.yaml", "columns.yaml", "functions.yaml", ".env"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was not written", n)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, ".env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf(".env must be created with mode 0600: %v %v", fi, err)
	}
	// the starter files must load and validate as they are
	c, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Validate(); len(errs) != 0 {
		t.Fatalf("the templates do not validate: %v", errs)
	}
	// the audit lists in the template are the built-in ones (so the docs and the code cannot drift apart)
	if c.Audit == nil || strings.Join(c.Audit.Names, "|") != strings.Join(config.DefaultAuditNames, "|") ||
		strings.Join(c.Audit.Types, "|") != strings.Join(engine.MySQL{}.FreeTextTypes(), "|") {
		t.Errorf("the audit section of config.yaml.dist differs from the built-in lists: %+v", c.Audit)
	}
	if len(c.Rules) == 0 || len(c.Funcs) == 0 {
		t.Errorf("the templates must show examples of columns and functions: %d / %d", len(c.Rules), len(c.Funcs))
	}
}

func TestWriteDoesNotOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "columns.yaml")
	if err := os.WriteFile(mine, []byte("mine: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, false); err == nil || !strings.Contains(err.Error(), "columns.yaml") {
		t.Fatalf("expected a refusal naming columns.yaml, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
		t.Error("a refused scaffold must not leave partial files behind")
	}
	if b, _ := os.ReadFile(mine); string(b) != "mine: 1\n" {
		t.Error("the existing file was modified")
	}
	if _, err := Write(dir, true); err != nil {
		t.Fatalf("--force must overwrite: %v", err)
	}
	if b, _ := os.ReadFile(mine); strings.Contains(string(b), "mine: 1") {
		t.Error("--force did not overwrite the file")
	}
}
