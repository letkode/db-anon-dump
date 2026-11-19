package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestOpenWriteSummary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	now := time.Date(2026, 11, 19, 15, 30, 45, 0, time.Local)
	l, err := Open(dir, "run", now, "dbanondump test")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(l.Path()) != "run-20261119-153045.log" {
		t.Errorf("name = %s", l.Path())
	}
	l.Phase("structure")
	l.Logf("reading %d tables", 3)
	l.Phase("anonymise")
	l.Close(7, errors.New("data extraction failed: boom\nsecond line"))

	fi, _ := os.Stat(l.Path())
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(l.Path())
	s := string(b)
	for _, want := range []string{"dbanondump test", "reading 3 tables", "---- summary ----", "command:  run",
		"result:   ERROR (exit 7)", "error:    data extraction failed: boom", "duration:", "structure", "anonymise"} {
		if !strings.Contains(s, want) {
			t.Errorf("log misses %q:\n%s", want, s)
		}
	}
	if !regexp.MustCompile(`(?m)^\d\d:\d\d:\d\d reading 3 tables$`).MatchString(s) {
		t.Errorf("lines must start with the time:\n%s", s)
	}
	l.Logf("after close must not panic")
}

func TestSuccessSummary(t *testing.T) {
	l, _ := Open(t.TempDir(), "check", time.Now())
	l.Close(0, nil)
	b, _ := os.ReadFile(l.Path())
	if !strings.Contains(string(b), "result:   OK (exit 0)") {
		t.Errorf("got:\n%s", b)
	}
}

func TestSecretsAreRedacted(t *testing.T) {
	l, _ := Open(t.TempDir(), "run", time.Now())
	l.SetSecrets(func() []string { return []string{"s3cr3t-pw", ""} })
	l.Logf("dial mysql://u:s3cr3t-pw@host failed")
	l.Close(7, errors.New("access denied for password s3cr3t-pw"))
	b, _ := os.ReadFile(l.Path())
	if strings.Contains(string(b), "s3cr3t-pw") {
		t.Errorf("the password reached the log:\n%s", b)
	}
	if !strings.Contains(string(b), "***") {
		t.Errorf("expected *** in the log:\n%s", b)
	}
}

func TestNilLogIsSafe(t *testing.T) {
	var l *Log
	l.Logf("x")
	l.Phase("p")
	l.SetSecrets(nil)
	l.Close(0, nil)
	if l.Path() != "" {
		t.Error("nil path must be empty")
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"run-20261101-100000.log", "run-20261102-100000.log", "check-20261103-100000.log", "run-20261104-100000.log", "notes.txt"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	if err := Prune(dir, 2); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	got := strings.Join(names, " ")
	if got != "check-20261103-100000.log notes.txt run-20261104-100000.log" {
		t.Errorf("left: %s", got)
	}
	if err := Prune(dir, 0); err != nil {
		t.Fatal(err)
	}
}
