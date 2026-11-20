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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/letkode/db-anon-dump/internal/config"
	"github.com/letkode/db-anon-dump/internal/engine"
	"github.com/letkode/db-anon-dump/internal/envfile"
	"github.com/letkode/db-anon-dump/internal/plan"
	"github.com/letkode/db-anon-dump/internal/runlog"
	"github.com/letkode/db-anon-dump/internal/suggest"
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
	Phase     func(name string) // called when a phase starts (for the time per phase in the run log)

	SchemaOnly []string      // table specs dumped with structure only; added to tables.schema_only
	Verbose    bool          // extra detail (tables, container, timings)
	Suggest    bool          // check: write columns.suggested.yaml from the audit
	AllowEmpty bool          // run: allow a columns.yaml with no rule (the dump keeps the real data)
	Heartbeat  time.Duration // how often a long step says it is alive (0 = 10s)
}

func (o *Options) phase(name string) {
	if o.Phase != nil {
		o.Phase(name)
	}
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
	aud, err := cfg.Auditor(d)
	if err != nil {
		return fail(ExitConfig, "%v", err)
	}
	for _, w := range cfg.Warnings() {
		o.logf("WARNING: %s", w)
	}
	schemaSpecs := append(append([]string{}, cfg.Tables.SchemaOnly...), o.SchemaOnly...)
	// with "*" no table carries data, so there is nothing for columns.yaml to protect
	allSchemaDeclared := false
	for _, s := range schemaSpecs {
		if s == "*" {
			allSchemaDeclared = true
		}
	}
	if len(cfg.Rules) == 0 && !o.CheckOnly && !o.AllowEmpty && !allSchemaDeclared {
		return fail(ExitConfig, "%s has no column to anonymise: the dump would contain the real data as is. If that is what you want, pass --allow-empty", filepath.Base(cfg.ColumnsPath))
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
		// .env, .env.local, .env.<APP_ENV>[.local] next to config.yaml; the real environment wins over them
		if _, err := envfile.Apply(cfg.Dir); err != nil {
			return fail(ExitUsage, "%v", err)
		}
		v, ok := os.LookupEnv(cfg.Source.PasswordEnv)
		if !ok {
			return fail(ExitUsage, "the source password must be in the %s environment variable (or in a .env file in %s)", cfg.Source.PasswordEnv, cfg.Dir)
		}
		srcPassword = v
	} else if _, err := os.Stat(dumpFile); err != nil {
		return fail(ExitUsage, "dump file: %v", err)
	}
	image := cfg.Temp.Image
	if image == "" {
		image = d.DefaultImage()
	}
	o.phase("temporary database")
	o.logf("starting temporary %s (%s)", d.Name(), image)
	c, err := tempdb.Start(ctx, d, image, cfg.Temp.Database, cfg.Temp.Tmpfs, cfg.Temp.ExtraArgs)
	if err != nil {
		return fail(ExitExtract, "%v", err)
	}
	defer func() {
		o.phase("cleanup")
		if o.Keep {
			o.logf("container %q kept (it holds REAL data). Remove it with: docker rm -f -v %s", c.Name, c.Name)
			return
		}
		c.Remove()
		o.logf("temporary container removed")
	}()
	o.vlogf("container %s is up, waiting for the database to accept connections", c.Name)
	ready := time.Now()
	db, err := c.WaitReady(ctx, 180*time.Second)
	if err != nil {
		return fail(ExitExtract, "%v", err)
	}
	defer db.Close()
	o.vlogf("temporary database ready after %s", runlog.Round(time.Since(ready)))

	// ---- which tables, and the structure ------------------------------------------------------------
	var tables []string
	var sizes map[string]int64 // approximate bytes per table of the source, for the data progress percentage
	o.phase("structure")
	if dumpFile != "" {
		o.logf("importing the dump file (whole file; tables that are not requested are dropped): %s", dumpFile)
		if err := importFile(ctx, c, dumpFile, o); err != nil {
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
		if err != nil {
			src.Close()
			return fail(ExitExtract, "cannot list the tables of the source: %v", err)
		}
		tables, err = config.SelectTables(universe, include, exclude)
		if err != nil {
			src.Close()
			return fail(ExitUsage, "%v", err)
		}
		if !o.CheckOnly {
			sizes = tableSizes(ctx, d, src, cfg.Source.Database, tables)
		}
		src.Close()
		o.logf("reading the structure of %d table(s) from %s:%d/%s (read-only)", len(tables), cfg.Source.Host, cfg.Source.Port, cfg.Source.Database)
		if err := c.Pipe(ctx, d.SecretEnv(srcPassword),
			d.SourceDumpCmd(containerHost(cfg), cfg.Source.Port, cfg.Source.User, cfg.Source.Database, tables, engine.DumpSchema),
			d.ClientCmd(c.Database), o.stream(d, "structure", len(tables), nil)); err != nil {
			return fail(ExitExtract, "structure extraction failed: %v", err)
		}
	}
	o.logf("tables to extract: %d", len(tables))
	o.vlogf("tables: %s", strings.Join(tables, ", "))

	// schema_only: these tables are dumped with their structure but no rows
	schemaOnly := config.MatchTables(tables, schemaSpecs)
	var dataTables []string
	for _, t := range tables {
		if !schemaOnly[t] {
			dataTables = append(dataTables, t)
		}
	}
	allSchema := len(dataTables) == 0
	for _, spec := range schemaSpecs {
		if len(config.MatchTables(tables, []string{spec})) == 0 {
			o.logf("WARNING: schema_only %q matches no extracted table (a typo, or the table is excluded / not in include)", spec)
		}
	}
	if len(schemaOnly) > 0 {
		o.logf("schema_only: %d of %d table(s) are dumped with their structure only (no rows)", len(schemaOnly), len(tables))
		o.vlogf("schema_only tables: %s", strings.Join(sortedKeys(schemaOnly), ", "))
		for t := range schemaOnly {
			delete(sizes, t)
		}
		if dumpFile != "" {
			if err := emptyTables(ctx, d, db, sortedKeys(schemaOnly)); err != nil {
				return fail(ExitExtract, "cannot empty the schema_only tables: %v", err)
			}
		}
	}

	o.phase("pre-flight")
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
	pl, errs := plan.Build(d, aud, tables, meta, cfg.Rules)
	if len(errs) > 0 {
		return configErrors(errs)
	}
	o.logf("pre-flight: checking %d pattern(s) against the real structure", countSets(pl))
	if errs := preflight(ctx, db, pl); len(errs) > 0 {
		return configErrors(errs)
	}
	// schema_only tables have no rows: nothing to anonymise there and nothing to audit
	if len(schemaOnly) > 0 {
		kept := pl.Tables[:0]
		for _, tp := range pl.Tables {
			if !schemaOnly[tp.Table] {
				kept = append(kept, tp)
			}
		}
		pl.Tables = kept
		audit := pl.Audit[:0]
		for _, a := range pl.Audit {
			if !schemaOnly[a.Table] {
				audit = append(audit, a)
			}
		}
		pl.Audit = audit
		for _, t := range sortedKeys(schemaOnly) {
			if len(cfg.Rules[t]) > 0 {
				o.logf("note: columns.yaml has patterns for %s, which is schema_only: they have no effect", t)
			}
		}
	}
	switch {
	case o.CheckOnly || o.Strict:
		if len(pl.Audit) > 0 {
			o.logf("%d column(s) look personal (JSON/text or a sensitive-looking name) and have NO pattern in the config:", len(pl.Audit))
			for _, a := range pl.Audit {
				o.logf("    %s", a.String())
			}
			o.logf("add a pattern, or `\"{KEEP}\"` as the pattern to mark a column as reviewed and left as is")
		}
		if o.Strict && len(pl.Audit) > 0 {
			return fail(ExitConfig, "--strict: %d unreviewed column(s)", len(pl.Audit))
		}
	case len(pl.Audit) > 0:
		o.logf("note: %d column(s) look personal but have no pattern (run `check` to list them)", len(pl.Audit))
	}
	if o.Suggest && o.CheckOnly {
		if len(pl.Audit) == 0 {
			o.logf("--suggest: every column that looks personal already has a pattern, nothing to suggest")
		} else {
			path := cfg.Path(suggest.FileName)
			note := fmt.Sprintf("Engine: %s. Generated %s for %d column(s) without a pattern.", d.Name(), time.Now().Format("2006-01-02 15:04"), len(pl.Audit))
			if err := suggest.Write(path, d, note, pl.Audit); err != nil {
				return fail(ExitUsage, "cannot write %s: %v", path, err)
			}
			o.logf("suggestions for %d column(s) written to %s (every line is commented out; review it, uncomment what you want and rename it to columns.yaml)", len(pl.Audit), path)
		}
	}
	if o.CheckOnly {
		o.logf("check only: %d table(s) have patterns; no data was loaded, nothing was written", len(pl.Tables))
		return nil
	}

	// ---- data -------------------------------------------------------------------------------------------
	o.phase("data extraction")
	if dumpFile == "" && allSchema {
		o.logf("no table carries data (schema_only): only the structure is dumped")
	}
	if dumpFile == "" && !allSchema {
		o.logf("extracting the data of %d table(s)", len(dataTables))
		if err := c.Pipe(ctx, d.SecretEnv(srcPassword),
			d.SourceDumpCmd(containerHost(cfg), cfg.Source.Port, cfg.Source.User, cfg.Source.Database, dataTables, engine.DumpData),
			d.ClientCmd(c.Database), o.stream(d, "data", len(dataTables), sizes)); err != nil {
			return fail(ExitExtract, "data extraction failed: %v", err)
		}
	}
	if errs := lengthCheck(ctx, db, pl); len(errs) > 0 {
		return configErrors(errs)
	}

	// ---- anonymise --------------------------------------------------------------------------------------
	if len(pl.Tables) > 0 {
		o.phase("anonymise")
		par := o.Parallel
		if par <= 0 {
			par = min(4, runtime.NumCPU())
		}
		o.logf("anonymising %d table(s) (%d in parallel)", len(pl.Tables), par)
		if err := anonymise(ctx, db, d, pl, par, o); err != nil {
			return err
		}
	}

	// ---- dump -------------------------------------------------------------------------------------------
	o.phase("dump")
	out := cfg.OutputPath(o.Output, time.Now())
	if allSchema && o.Output == "" { // never let a structure-only dump overwrite a dump with data
		out = config.SchemaOutput(out)
	}
	o.logf("writing the dump: %s", out)
	if err := writeDump(ctx, c, out, tables, o); err != nil {
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
		running  runningSet
		finished atomic.Int64
	)
	total := len(p.Tables)
	setErr := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	failed := func() bool { mu.Lock(); defer mu.Unlock(); return firstErr != nil }

	// heartbeat: which tables are being worked on right now
	stopBeat := make(chan struct{})
	beatDone := make(chan struct{})
	go func() {
		defer close(beatDone)
		t := time.NewTicker(o.every())
		defer t.Stop()
		for {
			select {
			case <-t.C:
				o.logf("  anonymising: %d/%d done, working on: %s", finished.Load(), total, running.describe())
			case <-stopBeat:
				return
			}
		}
	}()

	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tp := range jobs {
				if failed() {
					continue
				}
				started := time.Now()
				running.add(tp.Table)
				err := anonymiseTable(ctx, db, d, tp)
				running.remove(tp.Table)
				if err != nil {
					setErr(err)
					continue
				}
				o.logf("  ok  [%d/%d] %s (%s)", finished.Add(1), total, tp.Table, runlog.Round(time.Since(started)))
			}
		}()
	}
	for _, tp := range p.Tables {
		jobs <- tp
	}
	close(jobs)
	wg.Wait()
	close(stopBeat)
	<-beatDone
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

func importFile(ctx context.Context, c *tempdb.Container, path string, o Options) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var total int64
	if fi, err := f.Stat(); err == nil {
		total = fi.Size()
	}
	var read atomic.Int64
	defer o.watchBytes("import", total, &read)()
	var r io.Reader = countReader{f, &read}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	return c.Exec(ctx, r, nil, c.D.ClientCmd(c.Database))
}

func writeDump(ctx context.Context, c *tempdb.Container, path string, tables []string, o Options) (err error) {
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
	var written atomic.Int64
	defer o.watchBytes("dump", 0, &written)()
	var w io.Writer = countWriter{f, &written}
	var gz *gzip.Writer
	if strings.HasSuffix(path, ".gz") {
		gz = gzip.NewWriter(w)
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

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// emptyTables deletes every row of the given tables (used when the data came from a whole-database dump).
func emptyTables(ctx context.Context, d engine.Dialect, db *sql.DB, tables []string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, d.RelaxConstraints()); err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+d.QuoteIdent(t)); err != nil {
			return fmt.Errorf("%s: %v", t, err)
		}
	}
	return nil
}
