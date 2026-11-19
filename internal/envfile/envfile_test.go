package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// clean makes sure the given variables are unset now and after the test.
func clean(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "") // registers the restore
		os.Unsetenv(k)
	}
}

func TestFilesOrder(t *testing.T) {
	got := Files("d", "dev")
	want := []string{"d/.env", "d/.env.local", "d/.env.dev", "d/.env.dev.local"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	if n := len(Files("d", "")); n != 2 {
		t.Errorf("without APP_ENV only 2 files are read, got %d", n)
	}
}

func TestLayersAndPrecedence(t *testing.T) {
	clean(t, "APP_ENV", "T_A", "T_B", "T_C", "T_D", "T_E")
	dir := t.TempDir()
	put(t, dir, ".env", "APP_ENV=dev\nT_A=base\nT_B=base\nT_C=base\nT_D=base\n")
	put(t, dir, ".env.local", "T_B=local\nT_C=local\nT_D=local\n")
	put(t, dir, ".env.dev", "T_C=dev\nT_D=dev\n")
	put(t, dir, ".env.dev.local", "T_D=dev-local\n")
	put(t, dir, ".env.prod", "T_E=must-not-load\n")
	if _, err := Apply(dir); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"T_A": "base", "T_B": "local", "T_C": "dev", "T_D": "dev-local", "T_E": ""} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestRealEnvironmentWins(t *testing.T) {
	clean(t, "APP_ENV", "T_KEEP", "T_NEW")
	t.Setenv("T_KEEP", "from-env")
	t.Setenv("APP_ENV", "prod") // the real APP_ENV picks the layer, even if .env says dev
	dir := t.TempDir()
	put(t, dir, ".env", "APP_ENV=dev\nT_KEEP=from-file\nT_NEW=base\n")
	put(t, dir, ".env.prod", "T_NEW=prod\n")
	put(t, dir, ".env.dev", "T_NEW=dev\n")
	set, err := Apply(dir)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("T_KEEP") != "from-env" {
		t.Error("the real environment must win over the files")
	}
	if os.Getenv("T_NEW") != "prod" || len(set) != 1 {
		t.Errorf("T_NEW = %q, set = %v", os.Getenv("T_NEW"), set)
	}
}

func TestNoFilesIsNotAnError(t *testing.T) {
	if set, err := Apply(t.TempDir()); err != nil || len(set) != 0 {
		t.Fatalf("got %v, %v", set, err)
	}
}

func TestMalformedFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, ".env", "A='unterminated\n")
	if _, err := Apply(dir); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestDollarInPasswordNeedsSingleQuotes(t *testing.T) {
	clean(t, "APP_ENV", "T_SINGLE", "T_DOUBLE")
	dir := t.TempDir()
	put(t, dir, ".env", "T_SINGLE='pa$$word'\nT_DOUBLE=\"pa$$word\"\n")
	if _, err := Apply(dir); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("T_SINGLE"); got != "pa$$word" {
		t.Errorf("single quotes must keep the value literally, got %q", got)
	}
	if got := os.Getenv("T_DOUBLE"); got == "pa$$word" {
		t.Log("note: double quotes expanded nothing here")
	}
}
