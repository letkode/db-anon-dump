// Package plan builds, from the config and the real table structure, the SQL that anonymises each table.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/letkode/db-anon-dump/internal/config"
	"github.com/letkode/db-anon-dump/internal/engine"
	"github.com/letkode/db-anon-dump/internal/pattern"
)

// Set is one column assignment of a table's UPDATE.
type Set struct {
	Column  string
	Expr    string // already wrapped to keep NULLs where required
	MaxLen  int    // character limit of the column, 0 when it is not a string
	Pattern string // original pattern, for messages
}

// TablePlan is everything to run for one table.
type TablePlan struct {
	Table string
	Parks []string // UNIQUE string columns to park before the final UPDATE
	Sets  []Set
	d     engine.Dialect
}

// AuditCol is a column that looks personal but has no pattern.
type AuditCol struct {
	Table, Column, DataType string
	CharMax                 int  // character limit, 0 when it is not a string
	Nullable                bool // the column accepts NULL
	HasID                   bool // the table has an `id` column (patterns can use it)
}

// String is "table.column (type)".
func (a AuditCol) String() string { return fmt.Sprintf("%s.%s (%s)", a.Table, a.Column, a.DataType) }

// Plan is the result for all extracted tables.
type Plan struct {
	Tables []*TablePlan
	Audit  []AuditCol // columns that look personal but have no pattern, per the audit settings
}

// Build resolves, for each table in order, the pattern of each column: an exact table entry wins over the
// "*" wildcard. All problems are returned together. meta holds the columns of every extracted table.
func Build(d engine.Dialect, aud *config.AuditMatcher, order []string, meta map[string][]engine.Column, rules map[string]map[string]config.Rule) (*Plan, []error) {
	var errs []error
	p := &Plan{}
	inSet := map[string]bool{}
	for _, t := range order {
		inSet[t] = true
	}

	// exact entries of an extracted table must name a real column (a typo would leave PII untouched)
	tnames := make([]string, 0, len(rules))
	for t := range rules {
		tnames = append(tnames, t)
	}
	sort.Strings(tnames)
	for _, t := range tnames {
		if t == "*" || !inSet[t] {
			continue
		}
		have := map[string]bool{}
		for _, c := range meta[t] {
			have[c.Name] = true
		}
		cnames := make([]string, 0, len(rules[t]))
		for c := range rules[t] {
			cnames = append(cnames, c)
		}
		sort.Strings(cnames)
		for _, c := range cnames {
			if !have[c] && !rules[t][c].Optional {
				errs = append(errs, fmt.Errorf("columns.%s.%s: the column does not exist in the database (mark it `optional: true` if it only exists in some environments)", t, c))
			}
		}
	}

	for _, t := range order {
		tp := &TablePlan{Table: t, d: d}
		hasID := false
		for _, c := range meta[t] {
			if c.Name == "id" {
				hasID = true
			}
		}
		for _, col := range meta[t] {
			rule, ok := rules[t][col.Name]
			if !ok {
				rule, ok = rules["*"][col.Name]
			}
			if !ok {
				if aud.Looks(col.Name, col.DataType) {
					p.Audit = append(p.Audit, AuditCol{Table: t, Column: col.Name, DataType: col.DataType,
						CharMax: col.CharMax, Nullable: col.Nullable, HasID: hasID})
				}
				continue
			}
			res, err := pattern.Parse(rule.Pattern, d.QuoteString)
			if err != nil {
				errs = append(errs, fmt.Errorf("columns.%s.%s: %v", t, col.Name, err))
				continue
			}
			if res.Kind == pattern.Keep {
				continue
			}
			qcol := d.QuoteIdent(col.Name)
			expr := res.SQL
			switch {
			case res.Kind == pattern.Null && col.Nullable:
				expr = "NULL"
			case res.Kind == pattern.Null:
				expr = d.NotNullDefault(col.DataType)
			case col.Nullable:
				expr = d.NullCase(qcol, expr)
			}
			tp.Sets = append(tp.Sets, Set{Column: col.Name, Expr: expr, MaxLen: col.CharMax, Pattern: rule.Pattern})
			if col.Unique && d.IsString(col.DataType) {
				if !hasID {
					errs = append(errs, fmt.Errorf("columns.%s.%s: the column is UNIQUE and the table has no `id` column to park values with", t, col.Name))
				} else {
					tp.Parks = append(tp.Parks, col.Name)
				}
			}
		}
		if len(tp.Sets) > 0 {
			p.Tables = append(p.Tables, tp)
		}
	}
	return p, errs
}

// ParkSQL moves the current values of a UNIQUE column out of the way so the final values cannot collide
// with real ones that have not been rewritten yet.
func (t *TablePlan) ParkSQL(col string) string {
	return fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s IS NOT NULL",
		t.d.QuoteIdent(t.Table), t.d.QuoteIdent(col), t.d.ParkValue("id"), t.d.QuoteIdent(col))
}

// UpdateSQL is the single UPDATE that applies every pattern of the table.
func (t *TablePlan) UpdateSQL() string {
	sets := make([]string, len(t.Sets))
	for i, s := range t.Sets {
		sets[i] = t.d.QuoteIdent(s.Column) + " = " + s.Expr
	}
	return "UPDATE " + t.d.QuoteIdent(t.Table) + "\n   SET " + strings.Join(sets, ",\n       ")
}

// CheckSQL counts rows that do not match their pattern; it must return 0 after the UPDATE.
func (t *TablePlan) CheckSQL() string {
	conds := make([]string, len(t.Sets))
	for i, s := range t.Sets {
		conds[i] = t.d.NotSame(t.d.QuoteIdent(s.Column), s.Expr)
	}
	return "SELECT COUNT(*) FROM " + t.d.QuoteIdent(t.Table) + "\n WHERE " + strings.Join(conds, "\n    OR ")
}

// PreflightSQL evaluates one pattern against the real structure; it catches unknown functions or
// columns and type problems without modifying anything.
func (t *TablePlan) PreflightSQL(s Set) string {
	return "SELECT " + s.Expr + " FROM " + t.d.QuoteIdent(t.Table) + " LIMIT 1"
}

// LengthSQL returns the longest value the pattern would produce; compare it with the column limit.
func (t *TablePlan) LengthSQL(s Set) string {
	return "SELECT COALESCE(MAX(" + t.d.CharLength(s.Expr) + "), 0) FROM " + t.d.QuoteIdent(t.Table)
}
