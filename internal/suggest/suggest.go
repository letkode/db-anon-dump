// Package suggest writes columns.suggested.yaml: a starting point for columns.yaml built from the columns
// that `check` flagged. EVERY line is commented out, so nothing is anonymised (or approved) until a person
// reads it, uncomments what they want and renames the file.
package suggest

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/letkode/db-anon-dump/internal/engine"
	"github.com/letkode/db-anon-dump/internal/plan"
)

// FileName is written next to config.yaml.
const FileName = "columns.suggested.yaml"

type kind int

const (
	kDecide kind = iota
	kSecret
	kEmail
	kPhone
	kID
	kFirst
	kLast
	kAddress
	kBirth
	kIP
	kURL
)

// The first class that matches the column name wins. Plain "name" columns are NOT classified: they are
// people in some tables and catalogue entries in others, so the person has to decide.
var classes = []struct {
	k  kind
	re *regexp.Regexp
}{
	{kSecret, regexp.MustCompile(`(?i)pass(word|wd)?($|_)|secret|salt|hash|token|api_?key`)},
	{kEmail, regexp.MustCompile(`(?i)mail|correo`)},
	{kPhone, regexp.MustCompile(`(?i)phone|fono|celular|mobile|telefono`)},
	{kID, regexp.MustCompile(`(?i)(^|_)(rut|dni|passport|pasaporte|identification|identificacion|document)(_|$)`)},
	{kFirst, regexp.MustCompile(`(?i)first_?name|firstname|nombres?$|given_?name`)},
	{kLast, regexp.MustCompile(`(?i)last_?name|lastname|surname|apellido|paternal|maternal`)},
	{kAddress, regexp.MustCompile(`(?i)address|direccion|street|calle`)},
	{kBirth, regexp.MustCompile(`(?i)birth|nacimiento`)},
	{kIP, regexp.MustCompile(`(?i)(^|_)ip($|_)|ip_address`)},
	{kURL, regexp.MustCompile(`(?i)linkedin|url|link`)},
}

func classify(name string) kind {
	for _, c := range classes {
		if c.re.MatchString(name) {
			return c.k
		}
	}
	return kDecide
}

// Render builds the file for the engine d. cols must come ordered by table.
func Render(d engine.Dialect, auditNote string, cols []plan.AuditCol) string {
	var sb strings.Builder
	sb.WriteString(`## columns.suggested.yaml -- written by ` + "`dbanondump check --suggest`" + `.
## EVERY line below is commented out: nothing is anonymised until you decide.
##   1. Read each column. Uncomment (remove the leading "# ") the ones you want and fix the pattern.
##   2. Rename this file to columns.yaml, or copy the lines you want into the one you already have.
##   3. Run: dbanondump validate -c config.yaml  and  dbanondump check -c config.yaml
## The suggestions come only from the column NAME and TYPE. They are a starting point, not a review:
## a "decide" note means there is no safe automatic suggestion (free text, JSON, or an ambiguous name).
## {KEEP} would leave the real data in the dump; use it only for columns you have checked.
`)
	if auditNote != "" {
		sb.WriteString("## " + auditNote + "\n")
	}
	cur := ""
	for _, c := range cols {
		if c.Table != cur {
			cur = c.Table
			fmt.Fprintf(&sb, "\n# %s:\n", yamlKey(c.Table))
		}
		pat, note := suggestion(d, c)
		fmt.Fprintf(&sb, "#   %s: %q   ## %s\n", yamlKey(c.Column), pat, note)
	}
	return sb.String()
}

// Write renders the suggestions and saves them to path (0644: it holds column names, not data).
func Write(path string, d engine.Dialect, auditNote string, cols []plan.AuditCol) error {
	return os.WriteFile(path, []byte(Render(d, auditNote, cols)), 0o644)
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_$]+$`)

func yamlKey(s string) string {
	if plainKey.MatchString(s) {
		return s
	}
	return fmt.Sprintf("%q", s)
}

// fits is true when a value of at most n characters can be stored (n == 0: no known limit).
func fits(charMax, n int) bool { return charMax == 0 || charMax >= n }

func suggestion(d engine.Dialect, c plan.AuditCol) (pattern, note string) {
	typ := strings.ToLower(c.DataType)
	text := d.IsString(typ) || d.IsFreeText(typ)
	note = c.DataType
	if c.CharMax > 0 {
		note += fmt.Sprintf("(%d)", c.CharMax)
	}
	if d.IsFreeText(typ) {
		return "{NULL}", note + ": decide. {NULL} drops the content, {KEEP} leaves it, or mask only e-mails/phones inside it"
	}
	k := classify(c.Column)
	col := c.Column // how the pattern refers to the column: quoted when the name is not a plain identifier
	if !plainKey.MatchString(col) {
		col = d.QuoteIdent(col)
	}
	if k == kBirth && (typ == "date" || strings.HasPrefix(typ, "timestamp") || typ == "datetime") {
		if d.Name() == "postgres" {
			return fmt.Sprintf("{DATE_TRUNC('year', %s)::date}", col), note + ": keeps the year, hides the day"
		}
		return fmt.Sprintf("{MAKEDATE(YEAR(%s), 1)}", col), note + ": keeps the year, hides the day"
	}
	if !text {
		return "{KEEP}", note + ": not text, so probably a flag or a number: decide"
	}
	// Every pattern is checked after the UPDATE by evaluating it again on the new row, so it must give the same
	// value the second time: it may use `id` (never anonymised) but not hash or alter its own column.
	switch k {
	case kSecret, kEmail, kPhone, kID, kFirst, kLast, kAddress, kIP, kURL:
		if !c.HasID {
			return "{NULL}", note + ": the table has no `id` column to build a stable value from: decide"
		}
	default:
		return "{KEEP}", note + ": decide. Name or free text; use a pattern if it identifies a person"
	}
	// CONCAT(id, '') makes the id text: PostgreSQL's md5() does not accept an integer, MySQL's does not mind.
	// A short column cannot take a long fake value: a prefix of the hash of the id always fits
	short := func(need int) (string, string, bool) {
		if fits(c.CharMax, need) {
			return "", "", false
		}
		if c.CharMax < 1 {
			return "{NULL}", note + ": decide", true
		}
		return fmt.Sprintf("{LEFT(MD5(CONCAT(id, '')), %d)}", c.CharMax), note + ": column too short for a readable value, so a hash prefix", true
	}
	switch k {
	case kSecret:
		if p, n, ok := short(32); ok {
			return p, n
		}
		return "{MD5(CONCAT(id, ''))}", note + ": irreversible and unique (built from the id, so the check can repeat it)"
	case kIP:
		if p, n, ok := short(11); ok {
			return p, n
		}
		return "192.0.2.{(id % 254) + 1}", note + ": TEST-NET-1, a documentation-only range"
	case kURL:
		if p, n, ok := short(32); ok {
			return p, n
		}
		return "https://example.test/{id}", note
	}
	switch k {
	case kEmail:
		if p, n, ok := short(28); ok {
			return p, n
		}
		return "user-{id}@example.test", note
	case kPhone:
		if p, n, ok := short(13); ok {
			return p, n
		}
		return "+56900{(id % 1000000) + 1000000}", note + ": fake mobile number"
	case kID:
		if p, n, ok := short(13); ok {
			return p, n
		}
		return "ID-{id}", note + ": fake identifier"
	case kFirst:
		if p, n, ok := short(16); ok {
			return p, n
		}
		return "Nombre{id}", note
	case kLast:
		if p, n, ok := short(18); ok {
			return p, n
		}
		return "Apellido{id}", note
	case kAddress:
		if p, n, ok := short(18); ok {
			return p, n
		}
		return "Direccion {id}", note
	}
	return "{KEEP}", note + ": decide"
}
