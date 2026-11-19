// Package runlog writes one log file per run: every line printed to the console (with the time), the error if
// the run fails, and a closing summary with the total duration and the time spent in each phase.
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type phase struct {
	name string
	dur  time.Duration
}

// Log is the log of one run. All methods are safe on a nil *Log (they do nothing) and for concurrent use.
type Log struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	cmd      string
	start    time.Time
	phases   []phase
	cur      string
	curStart time.Time
	secrets  func() []string
}

// Path is the file the log is written to.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Open creates dir (if needed) and <dir>/<cmd>-<yyyymmdd-hhmmss>.log with mode 0600: error messages can
// quote fragments of real data. header lines are written first.
func Open(dir, cmd string, now time.Time, header ...string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.log", cmd, now.Format("20060102-150405")))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f, path: path, cmd: cmd, start: now}
	for _, h := range header {
		l.write(h)
	}
	return l, nil
}

// SetSecrets registers a function that returns values (the password) that must never reach the file: they are
// replaced by *** on every write. It is called on each write, so values loaded later are covered too.
func (l *Log) SetSecrets(f func() []string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.secrets = f
	l.mu.Unlock()
}

// Logf writes one line, prefixed with the time of day.
func (l *Log) Logf(format string, a ...any) {
	if l == nil {
		return
	}
	l.write(fmt.Sprintf(format, a...))
}

func (l *Log) write(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if l.secrets != nil {
		for _, s := range l.secrets() {
			if len(s) >= 3 {
				msg = strings.ReplaceAll(msg, s, "***")
			}
		}
	}
	fmt.Fprintf(l.f, "%s %s\n", time.Now().Format("15:04:05"), msg)
}

// Phase closes the running phase (if any) and starts a new one called name.
func (l *Log) Phase(name string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.endPhase(time.Now())
	l.cur, l.curStart = name, time.Now()
	l.mu.Unlock()
}

func (l *Log) endPhase(now time.Time) {
	if l.cur != "" {
		l.phases = append(l.phases, phase{l.cur, now.Sub(l.curStart)})
		l.cur = ""
	}
}

// Close writes the summary (result, exit code, error, total duration, time per phase) and closes the file.
func (l *Log) Close(code int, runErr error) {
	if l == nil {
		return
	}
	end := time.Now()
	l.mu.Lock()
	l.endPhase(end)
	phases := append([]phase(nil), l.phases...)
	l.mu.Unlock()

	var sb strings.Builder
	sb.WriteString("---- summary ----\n")
	fmt.Fprintf(&sb, "command:  %s\n", l.cmd)
	if code == 0 {
		sb.WriteString("result:   OK (exit 0)\n")
	} else {
		fmt.Fprintf(&sb, "result:   ERROR (exit %d)\n", code)
		if runErr != nil {
			fmt.Fprintf(&sb, "error:    %s\n", strings.ReplaceAll(runErr.Error(), "\n", "\n          "))
		}
	}
	fmt.Fprintf(&sb, "started:  %s\n", l.start.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&sb, "duration: %s\n", Round(end.Sub(l.start)))
	if len(phases) > 0 {
		sb.WriteString("phases:\n")
		for _, p := range phases {
			fmt.Fprintf(&sb, "  %-22s %s\n", p.name, Round(p.dur))
		}
	}
	l.write(strings.TrimRight(sb.String(), "\n"))

	l.mu.Lock()
	l.f.Close()
	l.f = nil
	l.mu.Unlock()
}

// Round formats a duration for humans: milliseconds below one second, otherwise rounded to 100ms.
func Round(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

// Prune keeps the newest keep logs of dir (<cmd>-<datetime>.log) and deletes the rest. keep <= 0 keeps all.
func Prune(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var logs []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".log") && (strings.HasPrefix(n, "run-") || strings.HasPrefix(n, "check-")) {
			logs = append(logs, n)
		}
	}
	if len(logs) <= keep {
		return nil
	}
	// the datetime in the name sorts chronologically within a command; order all by that part
	sort.Slice(logs, func(i, j int) bool { return stamp(logs[i]) < stamp(logs[j]) })
	for _, n := range logs[:len(logs)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	return nil
}

func stamp(name string) string { // run-20261119-153045.log -> 20261119-153045
	_, s, _ := strings.Cut(strings.TrimSuffix(name, ".log"), "-")
	return s
}
