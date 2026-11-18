// Package pipeline runs the whole extraction: temporary database, extraction, validation, anonymisation, dump.
package pipeline

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/letkode/db-anon-dump/internal/config"
	"github.com/letkode/db-anon-dump/internal/engine"
	"github.com/letkode/db-anon-dump/internal/plan"
	"github.com/letkode/db-anon-dump/internal/tempdb"
)

// Exit codes.
const (
	ExitUsage     = 2 // bad usage or config file
	ExitConfig    = 3 // validation / pre-flight problem, or --strict audit
	ExitAnonymise = 5 // an anonymisation statement failed
	ExitCheck     = 6 // a post-anonymisation check failed
	ExitExtract   = 7 // extraction / import failed
)

// ExitError carries the process exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func fail(code int, format string, a ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, a...)}
}

// Options come from the command line.
type Options struct {
	Tables    []string // specs; overrides tables.include of the config. Items starting with '!' are excludes
	Exclude   []string
	FromDump  string
	Output    string
	Parallel  int
	Keep      bool // keep the container (it holds REAL data)
	Strict    bool // abort when personal-looking columns have no pattern
	CheckOnly bool // validate + audit only; no data is loaded, nothing is written
	Log       func(format string, a ...any)
}

func (o *Options) logf(format string, a ...any) {
	if o.Log != nil {
		o.Log(format, a...)
	}
}

// Validate runs the offline checks only.
func Validate(cfg *config.Config) error {
	if errs := cfg.Validate(); len(errs) > 0 {
		return configErrors(errs)
	}
	return nil
}

func configErrors(errs []error) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d problem(s) found:", len(errs))
	for _, e := range errs {
		sb.WriteString("\n    - " + e.Error())
	}
	return &ExitError{Code: ExitConfig, Err: errors.New(sb.String())}
}

// Run executes the pipeline.
func Run(ctx context.Context, cfg *config.Config, o Options) (err error) {
	if err := Validate(cfg); err != nil {
		return err
	}
	d, err := engine.Get(cfg.Engine)
	if err != nil {
		return fail(ExitUsage, "%v", err)
	}

	include, exclude := cfg.Tables.Include, append([]string{}, cfg.Tables.Exclude...)
	if len(o.Tables) > 0 {
		include = nil
		for _, s := range o.Tables {
			if strings.HasPrefix(s, "!") {
				exclude = append(exclude, strings.TrimPrefix(s, "!"))
			} else {
				include = append(include, s)
			}
		}
		if len(include) == 0 {
			include = []string{"*"}
		}
	}
	exclude = append(exclude, o.Exclude...)

	dumpFile := o.FromDump
	if dumpFile == "" && cfg.Source.DumpFile != "" {
		dumpFile = cfg.Path(cfg.Source.DumpFile)
	}
	srcPassword := ""
	if dumpFile == "" {
		v, ok := os.LookupEnv(cfg.Source.PasswordEnv)
		if !ok {
			return fail(ExitUsage, "the source password must be in the %s environment variable", cfg.Source.PasswordEnv)
		}
		srcPassword = v
	} else if _, err := os.Stat(dumpFile); err != nil {
		return fail(ExitUsage, "dump file: %v", err)
	}
	image := cfg.Temp.Image
	if image == "" {
		image = d.DefaultImage()
	}
	o.logf("starting temporary %s (%s)", d.Name(), image)
	c, err := tempdb.Start(ctx, d, image, cfg.Temp.Database, cfg.Temp.Tmpfs, cfg.Temp.ExtraArgs)
	if err != nil {
		return fail(ExitExtract, "%v", err)
	}
	defer func() {
		if o.Keep {
			o.logf("container %q kept (it holds REAL data). Remove it with: docker rm -f -v %s", c.Name, c.Name)
			return
		}
		c.Remove()
		o.logf("temporary container removed")
	}()
	db, err := c.WaitReady(ctx, 180*time.Second)
	if err != nil {
		return fail(ExitExtract, "%v", err)
	}
	defer db.Close()

	// ---- which tables, and the structure ------------------------------------------------------------
	var tables []string
	if dumpFile != "" {
		o.logf("importing the dump file (whole file; tables that are not requested are dropped): %s", dumpFile)
		if err := importFile(ctx, c, dumpFile); err != nil {
			return fail(ExitExtract, "dump import failed: %v", err)
		}
		universe, err := listTables(ctx, d, db, c.Database)
		if err != nil {
			return fail(ExitExtract, "%v", err)
		}
		tables, err = config.SelectTables(universe, include, exclude)
		if err != nil {
			return fail(ExitUsage, "%v", err)
		}
		if err := dropOthers(ctx, d, db, universe, tables); err != nil {
			return fail(ExitExtract, "%v", err)
		}
	} else {
		src, err := sql.Open(d.DriverName(), d.DSN(cfg.Source.Host, cfg.Source.Port, cfg.Source.User, srcPassword, cfg.Source.Database))
		if err != nil {
			return fail(ExitExtract, "%v", err)
		}
		universe, err := listTables(ctx, d, src, cfg.Source.Database)
		src.Close()
		if err != nil {
			return fail(ExitExtract, "cannot list the tables of the source: %v", err)
		}
		tables, err = config.SelectTables(universe, include, exclude)
		if err != nil {
			return fail(ExitUsage, "%v", err)
		}
		o.logf("reading the structure of %d table(s) from %s:%d/%s (read-only)", len(tables), cfg.Source.Host, cfg.Source.Port, cfg.Source.Database)
		if err := c.Pipe(ctx, d.SecretEnv(srcPassword),
			d.SourceDumpCmd(containerHost(cfg), cfg.Source.Port, cfg.Source.User, cfg.Source.Database, tables, engine.DumpSchema),
			d.ClientCmd(c.Database)); err != nil {
			return fail(ExitExtract, "structure extraction failed: %v", err)
		}
	}
	o.logf("tables to extract: %d", len(tables))

	if len(cfg.Funcs) > 0 {
		o.logf("creating %d helper function(s)", len(cfg.Funcs))
		for _, f := range cfg.Funcs {
			if _, err := db.ExecContext(ctx, d.DropFunctionSQL(f.Name)); err != nil {
				return fail(ExitConfig, "functions: cannot drop %q: %v", f.Name, err)
			}
			if _, err := db.ExecContext(ctx, f.SQL); err != nil {
				return fail(ExitConfig, "functions: %q failed: %v", f.Name, err)
			}
		}
	}

	// ---- pre-flight: validate everything against the real structure BEFORE moving any data ------------
	meta, err := loadMeta(ctx, d, db, c.Database, tables)
	if err != nil {
		return fail(ExitExtract, "%v", err)
	}
	pl, errs := plan.Build(d, tables, meta, cfg.Rules)
	if len(errs) > 0 {
		return configErrors(errs)
	}
	o.logf("pre-flight: checking %d pattern(s) against the real structure", countSets(pl))
	if errs := preflight(ctx, db, pl); len(errs) > 0 {
		return configErrors(errs)
	}
	switch {
	case o.CheckOnly || o.Strict:
		if len(pl.Audit) > 0 {
			o.logf("%d column(s) look personal (JSON/text or a sensitive-looking name) and have NO pattern in the config:", len(pl.Audit))
			for _, a := range pl.Audit {
				o.logf("    %s", a)
			}
			o.logf("add a pattern, or `\"{KEEP}\"` as the pattern to mark a column as reviewed and left as is")
		}
		if o.Strict && len(pl.Audit) > 0 {
			return fail(ExitConfig, "--strict: %d unreviewed column(s)", len(pl.Audit))
		}
	case len(pl.Audit) > 0:
		o.logf("note: %d column(s) look personal but have no pattern (run `check` to list them)", len(pl.Audit))
	}
	if o.CheckOnly {
		o.logf("check only: %d table(s) have patterns; no data was loaded, nothing was written", len(pl.Tables))
		return nil
	}

	// ---- data -------------------------------------------------------------------------------------------
	if dumpFile == "" {
		o.logf("extracting the data of %d table(s)", len(tables))
		if err := c.Pipe(ctx, d.SecretEnv(srcPassword),
			d.SourceDumpCmd(containerHost(cfg), cfg.Source.Port, cfg.Source.User, cfg.Source.Database, tables, engine.DumpData),
			d.ClientCmd(c.Database)); err != nil {
			return fail(ExitExtract, "data extraction failed: %v", err)
		}
	}
	if errs := lengthCheck(ctx, db, pl); len(errs) > 0 {
		return configErrors(errs)
	}

	// ---- anonymise --------------------------------------------------------------------------------------
	par := o.Parallel
	if par <= 0 {
		par = min(4, runtime.NumCPU())
	}
	o.logf("anonymising %d table(s) (%d in parallel)", len(pl.Tables), par)
	if err := anonymise(ctx, db, d, pl, par, o); err != nil {
		return err
	}

	// ---- dump -------------------------------------------------------------------------------------------
	out := cfg.OutputPath(o.Output, time.Now())
	o.logf("writing the dump: %s", out)
	if err := writeDump(ctx, c, out, tables); err != nil {
		return fail(ExitExtract, "dump failed: %v", err)
	}
	o.logf("done: %d table(s) extracted, %d anonymised. The dump is anonymised but still INTERNAL: do not publish it.", len(tables), len(pl.Tables))
	return nil
}

func containerHost(cfg *config.Config) string {
	if cfg.Source.ContainerHost != "" {
		return cfg.Source.ContainerHost
	}
	return cfg.Source.Host
}

func countSets(p *plan.Plan) int {
	n := 0
	for _, t := range p.Tables {
		n += len(t.Sets)
	}
	return n
}

func listTables(ctx context.Context, d engine.Dialect, db *sql.DB, database string) ([]string, error) {
	rows, err := db.QueryContext(ctx, d.ListTablesSQL(database))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func dropOthers(ctx context.Context, d engine.Dialect, db *sql.DB, universe, keep []string) error {
	k := map[string]bool{}
	for _, t := range keep {
		k[t] = true
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, d.RelaxConstraints()); err != nil {
		return err
	}
	for _, t := range universe {
		if !k[t] {
			if _, err := conn.ExecContext(ctx, d.DropTableSQL(t)); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadMeta(ctx context.Context, d engine.Dialect, db *sql.DB, database string, tables []string) (map[string][]engine.Column, error) {
	rows, err := db.QueryContext(ctx, d.ColumnsSQL(database, tables))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	meta := map[string][]engine.Column{}
	for rows.Next() {
		var c engine.Column
		var nullable string
		var charMax, uniq int64
		if err := rows.Scan(&c.Table, &c.Name, &nullable, &c.DataType, &charMax, &uniq); err != nil {
			return nil, err
		}
		c.Nullable, c.CharMax, c.Unique = nullable == "YES", int(charMax), uniq > 0
		meta[c.Table] = append(meta[c.Table], c)
	}
	return meta, rows.Err()
}

func preflight(ctx context.Context, db *sql.DB, p *plan.Plan) []error {
	var errs []error
	for _, tp := range p.Tables {
		for _, s := range tp.Sets {
			rows, err := db.QueryContext(ctx, tp.PreflightSQL(s))
			if err != nil {
				errs = append(errs, fmt.Errorf("columns.%s.%s: the pattern %q does not run on this database: %v", tp.Table, s.Column, s.Pattern, err))
				continue
			}
			rows.Close()
		}
	}
	return errs
}

func lengthCheck(ctx context.Context, db *sql.DB, p *plan.Plan) []error {
	var errs []error
	for _, tp := range p.Tables {
		for _, s := range tp.Sets {
			if s.MaxLen <= 0 {
				continue
			}
			var n int64
			if err := db.QueryRowContext(ctx, tp.LengthSQL(s)).Scan(&n); err != nil {
				errs = append(errs, fmt.Errorf("columns.%s.%s: cannot measure the pattern length: %v", tp.Table, s.Column, err))
				continue
			}
			if int(n) > s.MaxLen {
				errs = append(errs, fmt.Errorf("columns.%s.%s: the pattern %q produces %d characters but the column holds %d", tp.Table, s.Column, s.Pattern, n, s.MaxLen))
			}
		}
	}
	return errs
}

func anonymise(ctx context.Context, db *sql.DB, d engine.Dialect, p *plan.Plan, parallel int, o Options) error {
	jobs := make(chan *plan.TablePlan)
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	setErr := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	failed := func() bool { mu.Lock(); defer mu.Unlock(); return firstErr != nil }

	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tp := range jobs {
				if failed() {
					continue
				}
				if err := anonymiseTable(ctx, db, d, tp); err != nil {
					setErr(err)
					continue
				}
				o.logf("  ok  %s", tp.Table)
			}
		}()
	}
	for _, tp := range p.Tables {
		jobs <- tp
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

func anonymiseTable(ctx context.Context, db *sql.DB, d engine.Dialect, tp *plan.TablePlan) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fail(ExitAnonymise, "%s: %v", tp.Table, err)
	}
	defer conn.Close()
	// Foreign keys are relaxed because the extracted subset may reference tables that were not extracted.
	if _, err := conn.ExecContext(ctx, d.RelaxConstraints()); err != nil {
		return fail(ExitAnonymise, "%s: %v", tp.Table, err)
	}
	for _, col := range tp.Parks {
		if _, err := conn.ExecContext(ctx, tp.ParkSQL(col)); err != nil {
			return fail(ExitAnonymise, "anonymisation failed for %q (parking UNIQUE column %s): %v", tp.Table, col, err)
		}
	}
	if _, err := conn.ExecContext(ctx, tp.UpdateSQL()); err != nil {
		return fail(ExitAnonymise, "anonymisation failed for %q: %v\n%s", tp.Table, err, tp.UpdateSQL())
	}
	var bad int64
	if err := conn.QueryRowContext(ctx, tp.CheckSQL()).Scan(&bad); err != nil {
		return fail(ExitAnonymise, "the check could not run for %q: %v", tp.Table, err)
	}
	if bad != 0 {
		return fail(ExitCheck, "check failed for %q: %d row(s) do not match their pattern (is a pattern non-deterministic?)", tp.Table, bad)
	}
	return nil
}

func importFile(ctx context.Context, c *tempdb.Container, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	return c.Exec(ctx, r, nil, c.D.ClientCmd(c.Database))
}

func writeDump(ctx context.Context, c *tempdb.Container, path string, tables []string) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(path) // never leave a partial dump behind
		}
	}()
	var w io.Writer = f
	var gz *gzip.Writer
	if strings.HasSuffix(path, ".gz") {
		gz = gzip.NewWriter(f)
		w = gz
	}
	if err = c.Exec(ctx, nil, w, c.D.LocalDumpCmd(c.Database, tables)); err != nil {
		return err
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}
