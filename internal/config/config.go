// Package config loads and validates the three YAML files of a project:
//
//	config.yaml     connection, engine, temporary database, output, tables   (changes per environment)
//	columns.yaml    which columns to anonymise and how                        (changes with the schema)
//	functions.yaml  helper SQL functions the patterns may call                (rarely changes)
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/letkode/db-anon-dump/internal/engine"
	"github.com/letkode/db-anon-dump/internal/pattern"

	"gopkg.in/yaml.v3"
)

// Rule is the anonymisation of one column. In columns.yaml it is a string ("pattern") or a mapping
// ({pattern: "...", optional: true}).
type Rule struct {
	Pattern  string `yaml:"pattern"`
	Optional bool   `yaml:"optional"`
}

// UnmarshalYAML accepts both the short (string) and the long (mapping) form.
func (r *Rule) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		r.Pattern = s
		return nil
	}
	type plain Rule
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*r = Rule(p)
	return nil
}

// Function is one helper SQL function. SQL must be a single statement (no DELIMITER needed); the program
// drops the function by Name before creating it.
type Function struct {
	Name string `yaml:"name"`
	SQL  string `yaml:"sql"`
}

type Source struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	User        string `yaml:"user"`
	Database    string `yaml:"database"`
	PasswordEnv string `yaml:"password_env"`
	DumpFile    string `yaml:"dump_file"`
	// ContainerHost is the host name of the source as seen from inside the temporary container
	// (e.g. host.docker.internal for a database on this machine). Defaults to Host.
	ContainerHost string `yaml:"container_host"`
}

type Temp struct {
	Image     string   `yaml:"image"`
	Database  string   `yaml:"database"`
	Tmpfs     bool     `yaml:"tmpfs"`
	ExtraArgs []string `yaml:"extra_args"`
}

type Tables struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Config is config.yaml plus the contents of the two files it points to.
type Config struct {
	Version   int    `yaml:"version"`
	Engine    string `yaml:"engine"`
	Source    Source `yaml:"source"`
	Temp      Temp   `yaml:"temp"`
	Output    string `yaml:"output"`
	Tables    Tables `yaml:"tables"`
	Columns   string `yaml:"columns"`   // path to columns.yaml (default: columns.yaml next to config.yaml)
	Functions string `yaml:"functions"` // path to functions.yaml (default: functions.yaml next to config.yaml, optional)

	// Loaded from the files above.
	Rules map[string]map[string]Rule `yaml:"-"`
	Funcs []Function                 `yaml:"-"`

	Dir           string `yaml:"-"` // directory of config.yaml; relative paths resolve against it
	ColumnsPath   string `yaml:"-"`
	FunctionsPath string `yaml:"-"`
}

var tableSpecRe = regexp.MustCompile(`^!?[A-Za-z0-9_*?]+$`)
var identRe = regexp.MustCompile(`^(\*|[A-Za-z0-9_$]+)$`)
var colRe = regexp.MustCompile(`^[A-Za-z0-9_$]+$`)
var funcNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var placeholderRe = regexp.MustCompile(`\{([^}]*)\}`)

// DefaultOutput names the dump after the day it was made.
const DefaultOutput = "output/dev-dump-{date}.sql.gz"

func decode(path string, strict bool, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(strict)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) { // io.EOF = empty file
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Load reads config.yaml (unknown keys are errors) and the columns and functions files it points to.
func Load(path string) (*Config, error) {
	var c Config
	if err := decode(path, true, &c); err != nil {
		return nil, err
	}
	abs, _ := filepath.Abs(path)
	c.Dir = filepath.Dir(abs)
	if c.Source.Port == 0 {
		c.Source.Port = 3306
		if e := strings.ToLower(c.Engine); e == "postgres" || e == "postgresql" {
			c.Source.Port = 5432
		}
	}
	if c.Source.PasswordEnv == "" {
		c.Source.PasswordEnv = "DBANONDUMP_SOURCE_PASSWORD"
	}
	if c.Temp.Database == "" {
		c.Temp.Database = "anon_tmp"
	}
	if c.Output == "" {
		c.Output = DefaultOutput
	}

	// columns.yaml: required
	cols := c.Columns
	if cols == "" {
		cols = "columns.yaml"
	}
	c.ColumnsPath = c.Path(cols)
	if _, err := os.Stat(c.ColumnsPath); err != nil {
		return nil, fmt.Errorf("columns file not found: %s (set `columns:` in %s)", c.ColumnsPath, filepath.Base(path))
	}
	if err := decode(c.ColumnsPath, false, &c.Rules); err != nil {
		return nil, err
	}

	// functions.yaml: optional by default, required when named explicitly
	fn := c.Functions
	explicit := fn != ""
	if !explicit {
		fn = "functions.yaml"
	}
	c.FunctionsPath = c.Path(fn)
	if _, err := os.Stat(c.FunctionsPath); err != nil {
		if explicit {
			return nil, fmt.Errorf("functions file not found: %s", c.FunctionsPath)
		}
		c.FunctionsPath = ""
	} else if err := decode(c.FunctionsPath, true, &c.Funcs); err != nil {
		return nil, err
	}
	return &c, nil
}

// Path resolves a path from the config relative to the config file.
func (c *Config) Path(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Dir, p)
}

// ExpandOutput replaces {date} (2026-11-18) and {datetime} (20261118-153045) in an output name.
func ExpandOutput(s string, t time.Time) string {
	s = strings.ReplaceAll(s, "{date}", t.Format("2006-01-02"))
	return strings.ReplaceAll(s, "{datetime}", t.Format("20060102-150405"))
}

// OutputPath is the dump file for this run: the override (command line) or config.yaml's output, with
// the date placeholders expanded and relative paths resolved against config.yaml.
func (c *Config) OutputPath(override string, now time.Time) string {
	out := c.Output
	if override != "" {
		out = override
	}
	return c.Path(ExpandOutput(out, now))
}

// Validate performs every check that does not need a database. It returns ALL problems at once.
func (c *Config) Validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	colsFile := filepath.Base(c.ColumnsPath)

	if c.Version != 1 {
		add("config: version must be 1 (got %d)", c.Version)
	}
	d, err := engine.Get(c.Engine)
	if err != nil {
		add("config: engine: %v", err)
	}
	if c.Source.DumpFile == "" {
		if c.Source.Host == "" || c.Source.User == "" || c.Source.Database == "" {
			add("config: source: set host, user and database (or source.dump_file); the password goes in the %s environment variable", c.Source.PasswordEnv)
		}
	}
	for _, s := range append(append([]string{}, c.Tables.Include...), prefixAll("!", c.Tables.Exclude)...) {
		if !tableSpecRe.MatchString(s) {
			add("config: tables: invalid table spec %q (use a name, a glob with * or ?, or * for all)", s)
		}
	}
	if len(c.Tables.Include) == 0 {
		add("config: tables.include: at least one table spec is required (use \"*\" for all)")
	}
	for _, m := range placeholderRe.FindAllStringSubmatch(c.Output, -1) {
		if m[1] != "date" && m[1] != "datetime" {
			add("config: output: unknown placeholder {%s} (use {date} or {datetime})", m[1])
		}
	}

	if len(c.Rules) == 0 {
		add("%s: no column to anonymise: the dump would contain the data as is", colsFile)
	}
	tables := make([]string, 0, len(c.Rules))
	for t := range c.Rules {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		if !identRe.MatchString(t) {
			add("%s: invalid table name %q", colsFile, t)
		}
		cols := make([]string, 0, len(c.Rules[t]))
		for col := range c.Rules[t] {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, col := range cols {
			if !colRe.MatchString(col) {
				add("%s: %s: invalid column name %q", colsFile, t, col)
				continue
			}
			if d == nil {
				continue
			}
			if _, perr := pattern.Parse(c.Rules[t][col].Pattern, d.QuoteString); perr != nil {
				add("%s: %s.%s: %v (pattern %q)", colsFile, t, col, perr, c.Rules[t][col].Pattern)
			}
		}
	}

	seen := map[string]bool{}
	for i, f := range c.Funcs {
		fnFile := filepath.Base(c.FunctionsPath)
		switch {
		case !funcNameRe.MatchString(f.Name):
			add("%s: entry %d: invalid function name %q", fnFile, i+1, f.Name)
		case seen[f.Name]:
			add("%s: function %q is defined twice", fnFile, f.Name)
		case strings.TrimSpace(f.SQL) == "":
			add("%s: function %q has no sql", fnFile, f.Name)
		}
		seen[f.Name] = true
	}
	return errs
}

func prefixAll(p string, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = p + s
	}
	return out
}

// SelectTables applies the include/exclude specs to the tables that exist. Excludes always win. A literal
// include that does not exist is an error (a typo must not silently drop a table).
func SelectTables(universe, include, exclude []string) ([]string, error) {
	inc := compile(include)
	exc := compile(exclude)
	var out []string
	for _, t := range universe {
		if matchAny(inc, t) && !matchAny(exc, t) {
			out = append(out, t)
		}
	}
	set := map[string]bool{}
	for _, t := range universe {
		set[t] = true
	}
	for _, s := range include {
		if !strings.ContainsAny(s, "*?") && !set[s] {
			return nil, fmt.Errorf("table %q does not exist in the source", s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the table specs match no table")
	}
	return out, nil
}

func compile(specs []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, s := range specs {
		re := regexp.QuoteMeta(s)
		re = strings.ReplaceAll(re, `\*`, `.*`)
		re = strings.ReplaceAll(re, `\?`, `.`)
		out = append(out, regexp.MustCompile("^"+re+"$"))
	}
	return out
}

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, r := range res {
		if r.MatchString(s) {
			return true
		}
	}
	return false
}
