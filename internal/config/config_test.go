package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodConfig = `
version: 1
engine: mysql
source: {host: db, user: ro, database: app}
tables:
  include: ["*"]
  exclude: [mail, job_*]
`

const goodColumns = `
user:
  email: "user-{id}@example.test"
  plain_password: {pattern: "{NULL}", optional: true}
"*":
  phone: "{NULL}"
`

const goodFunctions = `
- name: fake_one
  sql: |
    CREATE FUNCTION fake_one() RETURNS INT DETERMINISTIC NO SQL RETURN 1
`

// project writes the given files into a temp dir and returns the path of config.yaml.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "config.yaml")
}

func TestLoadThreeFiles(t *testing.T) {
	c, err := Load(project(t, map[string]string{"config.yaml": goodConfig, "columns.yaml": goodColumns, "functions.yaml": goodFunctions}))
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !c.Rules["user"]["plain_password"].Optional || c.Rules["user"]["email"].Pattern == "" || c.Rules["*"]["phone"].Pattern != "{NULL}" {
		t.Errorf("rules not decoded: %+v", c.Rules)
	}
	if len(c.Funcs) != 1 || c.Funcs[0].Name != "fake_one" {
		t.Errorf("functions not decoded: %+v", c.Funcs)
	}
	if c.Source.PasswordEnv != "DBANONDUMP_SOURCE_PASSWORD" || c.Source.Port != 3306 || c.Output != DefaultOutput {
		t.Errorf("defaults not applied: %+v / %s", c.Source, c.Output)
	}
}

func TestFunctionsAreOptionalByDefaultButRequiredWhenNamed(t *testing.T) {
	c, err := Load(project(t, map[string]string{"config.yaml": goodConfig, "columns.yaml": goodColumns}))
	if err != nil || len(c.Funcs) != 0 {
		t.Fatalf("no functions.yaml must be fine: %v %v", err, c)
	}
	_, err = Load(project(t, map[string]string{"config.yaml": goodConfig + "functions: nope.yaml\n", "columns.yaml": goodColumns}))
	if err == nil || !strings.Contains(err.Error(), "functions file not found") {
		t.Errorf("an explicit functions file that is missing must be an error: %v", err)
	}
}

func TestColumnsFileIsRequired(t *testing.T) {
	_, err := Load(project(t, map[string]string{"config.yaml": goodConfig}))
	if err == nil || !strings.Contains(err.Error(), "columns file not found") {
		t.Errorf("got %v", err)
	}
	// a custom path is honoured
	p := project(t, map[string]string{"config.yaml": goodConfig + "columns: other.yaml\n", "other.yaml": goodColumns})
	if c, err := Load(p); err != nil || c.Rules["user"] == nil {
		t.Errorf("custom columns path: %v", err)
	}
}

func TestUnknownKeyInConfigIsAnError(t *testing.T) {
	if _, err := Load(project(t, map[string]string{"config.yaml": goodConfig + "surprise: 1\n", "columns.yaml": goodColumns})); err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if _, err := Load(project(t, map[string]string{"config.yaml": goodConfig, "columns.yaml": goodColumns,
		"functions.yaml": "- {name: f, sql: x, surprise: 1}\n"})); err == nil {
		t.Fatal("expected an error for an unknown key in functions.yaml")
	}
}

func TestValidateCollectsEverythingAndNamesTheFile(t *testing.T) {
	c, err := Load(project(t, map[string]string{
		"config.yaml":    "version: 2\nengine: mysql\ntables: {include: [\"bad name!\"]}\noutput: \"x-{when}.sql\"\n",
		"columns.yaml":   "user:\n  email: \"x-{id\"\n",
		"functions.yaml": "- {name: \"bad name\", sql: \"x\"}\n- {name: ok, sql: \"\"}\n- {name: ok, sql: \"y\"}\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, e := range c.Validate() {
		text += e.Error() + "\n"
	}
	for _, want := range []string{"config: version", "config: source", "invalid table spec",
		"unknown placeholder {when}", "columns.yaml: user.email", "functions.yaml: entry 1", "has no sql", "defined twice"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestUnsupportedEngineIsReported(t *testing.T) {
	c, err := Load(project(t, map[string]string{"config.yaml": strings.Replace(goodConfig, "mysql", "oracle", 1), "columns.yaml": goodColumns}))
	if err != nil {
		t.Fatal(err)
	}
	errs := c.Validate()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "config: engine") {
		t.Errorf("got %v", errs)
	}
}

func TestEmptyColumnsIsAnError(t *testing.T) {
	c, err := Load(project(t, map[string]string{"config.yaml": goodConfig, "columns.yaml": "# nothing yet\n"}))
	if err != nil {
		t.Fatal(err)
	}
	errs := c.Validate()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "no column to anonymise") {
		t.Errorf("got %v", errs)
	}
}

func TestPostgresDefaultPort(t *testing.T) {
	c, err := Load(project(t, map[string]string{"config.yaml": strings.Replace(goodConfig, "mysql", "postgres", 1), "columns.yaml": goodColumns}))
	if err != nil || c.Source.Port != 5432 {
		t.Errorf("port = %v, err = %v", c.Source.Port, err)
	}
}

func TestOutputPath(t *testing.T) {
	c, err := Load(project(t, map[string]string{"config.yaml": goodConfig, "columns.yaml": goodColumns}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 15, 30, 45, 0, time.UTC)
	if got := c.OutputPath("", now); got != filepath.Join(c.Dir, "output/dev-dump-2026-11-18.sql.gz") {
		t.Errorf("default output = %s", got)
	}
	if got := c.OutputPath("/tmp/x-{datetime}.sql", now); got != "/tmp/x-20261118-153045.sql" {
		t.Errorf("override output = %s", got)
	}
}

func TestSelectTables(t *testing.T) {
	u := []string{"user", "user_profile", "mail", "job_offer", "job_search"}
	got, err := SelectTables(u, []string{"*"}, []string{"mail", "job_*"})
	if err != nil || strings.Join(got, ",") != "user,user_profile" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := SelectTables(u, []string{"nope"}, nil); err == nil {
		t.Error("a literal table that does not exist must be an error")
	}
	got, _ = SelectTables(u, []string{"user_*"}, nil)
	if strings.Join(got, ",") != "user_profile" {
		t.Errorf("glob: got %v", got)
	}
	if _, err := SelectTables(u, []string{"user"}, []string{"user"}); err == nil {
		t.Error("everything excluded must be an error")
	}
}
