// dbanondump copies tables from a SQL database into a throwaway container, anonymises the columns listed
// in columns.yaml and writes a development dump.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/letkode/db-anon-dump/internal/config"
	"github.com/letkode/db-anon-dump/internal/pipeline"
	"github.com/letkode/db-anon-dump/internal/runlog"
	"github.com/letkode/db-anon-dump/internal/scaffold"
)

var version = "dev" // set at build time: -ldflags "-X main.version=..."

const usage = `dbanondump - extract tables into a throwaway database, anonymise them, write a dev dump

Usage:
  dbanondump init     [dir]     write starter config.yaml, columns.yaml, functions.yaml and .env (default: .)
  dbanondump run      [flags]   extract, anonymise and write the dump
  dbanondump check    [flags]   validate against the real structure and list personal-looking columns
                                without a pattern (loads no data, writes nothing)
  dbanondump validate [flags]   offline lint of the config file (no Docker, no database)
  dbanondump version

init flags:
      --force           overwrite the files if they already exist

Common flags:
  -c, --config FILE     config.yaml (default ./config.yaml); it points to columns.yaml and functions.yaml

run / check flags:
  -t, --tables LIST     comma-separated table specs, overrides tables.include ('!name' excludes)
  -x, --exclude LIST    comma-separated tables/globs to exclude (added to the config)
      --from-dump FILE  import this dump (.sql / .sql.gz) instead of reading the source
      --strict          exit 3 if a personal-looking column has no pattern
      --log-dir DIR     folder for the run log (default: logs/ next to config.yaml, see logs: in config.yaml)
      --no-log          do not write a run log

run flags:
  -o, --output FILE     output dump; '.gz' compresses it; {date} / {datetime} are expanded
                        (default: output in config.yaml)
      --parallel N      tables anonymised at the same time (default min(4, CPUs))
      --keep-container  keep the temporary container (it holds REAL data until you remove it)

Exit codes: 0 ok | 2 usage | 3 config / pre-flight problem or --strict | 5 anonymisation failed
            6 a check failed | 7 extraction or import failed
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, "[dbanondump] "+format+"\n", a...) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return pipeline.ExitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	case "version", "--version":
		fmt.Println("dbanondump", version)
		return 0
	case "init":
		return runInit(rest)
	case "run", "check", "validate":
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return pipeline.ExitUsage
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var (
		cfgPath, tables, exclude, fromDump, output, logDir string
		parallel                                           int
		strict, keep, noLog                                bool
	)
	for _, n := range []string{"c", "config"} {
		fs.StringVar(&cfgPath, n, "config.yaml", "config file")
	}
	if cmd != "validate" {
		for _, n := range []string{"t", "tables"} {
			fs.StringVar(&tables, n, "", "table specs")
		}
		for _, n := range []string{"x", "exclude"} {
			fs.StringVar(&exclude, n, "", "excluded tables")
		}
		fs.StringVar(&fromDump, "from-dump", "", "dump file")
		fs.BoolVar(&strict, "strict", false, "fail on unreviewed columns")
		fs.StringVar(&logDir, "log-dir", "", "folder for the run log")
		fs.BoolVar(&noLog, "no-log", false, "do not write a run log")
	}
	if cmd == "run" {
		for _, n := range []string{"o", "output"} {
			fs.StringVar(&output, n, "", "output file")
		}
		fs.IntVar(&parallel, "parallel", 0, "parallel tables")
		fs.BoolVar(&keep, "keep-container", false, "keep the container")
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return pipeline.ExitUsage
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logf("ERROR: %v", err)
		return pipeline.ExitUsage
	}
	if cmd == "validate" {
		if err := pipeline.Validate(cfg); err != nil {
			logf("ERROR: %v", err)
			return exitCode(err)
		}
		logf("%s is valid (engine: %s, %d table(s) with patterns, %d function(s))", cfgPath, cfg.Engine, len(cfg.Rules), len(cfg.Funcs))
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// run log: logs/<command>-<datetime>.log with everything printed here, the error and the durations
	lg := logf
	var rl *runlog.Log
	dir := cfg.LogDir()
	if logDir != "" {
		dir = logDir
	}
	if !noLog && dir != "" {
		var err error
		rl, err = runlog.Open(dir, cmd, time.Now(), "dbanondump "+version+" "+cmd+" "+strings.Join(rest, " "))
		if err != nil {
			logf("warning: cannot write the run log in %s: %v (continuing without it)", dir, err)
		} else {
			rl.SetSecrets(func() []string { return []string{os.Getenv(cfg.Source.PasswordEnv)} })
			if err := runlog.Prune(dir, cfg.Logs.Keep); err != nil {
				logf("warning: cannot prune old logs in %s: %v", dir, err)
			}
			lg = func(format string, a ...any) { logf(format, a...); rl.Logf(format, a...) }
		}
	}

	opts := pipeline.Options{
		Tables: split(tables), Exclude: split(exclude), FromDump: fromDump, Output: output,
		Parallel: parallel, Keep: keep, Strict: strict, CheckOnly: cmd == "check", Log: lg, Phase: rl.Phase,
	}
	code := 0
	runErr := pipeline.Run(ctx, cfg, opts)
	if runErr != nil {
		lg("ERROR: %v", runErr)
		code = exitCode(runErr)
	}
	rl.Close(code, runErr)
	if rl != nil {
		logf("log: %s", rl.Path())
	}
	return code
}

// runInit writes the starter files. Usage: dbanondump init [--force] [dir]
func runInit(args []string) int {
	dir, force := ".", false
	for _, a := range args {
		switch {
		case a == "--force" || a == "-f":
			force = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q for init\n\n%s", a, usage)
			return pipeline.ExitUsage
		default:
			dir = a
		}
	}
	written, err := scaffold.Write(dir, force)
	if err != nil {
		logf("ERROR: %v", err)
		return pipeline.ExitUsage
	}
	for _, w := range written {
		logf("created %s", w)
	}
	logf("next: edit them, put the password in .env (DBANONDUMP_SOURCE_PASSWORD), then run:")
	logf("  dbanondump validate -c %s", filepath.Join(dir, "config.yaml"))
	logf("  dbanondump check    -c %s", filepath.Join(dir, "config.yaml"))
	logf("  dbanondump run      -c %s", filepath.Join(dir, "config.yaml"))
	return 0
}

func exitCode(err error) int {
	var ee *pipeline.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 1
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
