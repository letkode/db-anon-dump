// Package pattern turns a column pattern ("user-{id}@example.test") into a SQL expression.
//
// A pattern is literal text with SQL expressions between braces. Everything outside the braces is a
// literal; everything inside is copied verbatim as SQL (the engine declared in the config decides which
// functions exist). A pattern that is exactly one expression keeps that expression's own type.
package pattern

import (
	"fmt"
	"strings"
)

// Kind tells the planner how to treat the result.
type Kind int

const (
	Expr Kind = iota // a normal SQL expression
	Null             // {NULL}: replace by NULL (or a type default when the column is NOT NULL)
	Keep             // {KEEP}: reviewed, leave the column untouched
)

// Result of parsing one pattern.
type Result struct {
	SQL  string
	Kind Kind
}

type part struct {
	text string
	expr bool
}

// Parse converts the pattern. quote renders a literal as a SQL string for the target engine.
func Parse(p string, quote func(string) string) (Result, error) {
	var (
		parts   []part
		buf     strings.Builder
		inExpr  bool
		quoteCh rune
		depth   int
	)
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		ch := rs[i]
		if !inExpr {
			switch ch {
			case '{':
				if buf.Len() > 0 {
					parts = append(parts, part{buf.String(), false})
					buf.Reset()
				}
				inExpr, depth, quoteCh = true, 1, 0
			case '}':
				return Result{}, fmt.Errorf("unmatched '}' at position %d", i+1)
			default:
				buf.WriteRune(ch)
			}
			continue
		}
		if quoteCh != 0 { // inside a SQL string: braces are plain text
			buf.WriteRune(ch)
			if ch == '\\' && i+1 < len(rs) {
				i++
				buf.WriteRune(rs[i])
			} else if ch == quoteCh {
				quoteCh = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quoteCh = ch
			buf.WriteRune(ch)
		case '{':
			depth++
			buf.WriteRune(ch)
		case '}':
			depth--
			if depth == 0 {
				e := strings.TrimSpace(buf.String())
				if e == "" {
					return Result{}, fmt.Errorf("empty expression '{}' at position %d", i+1)
				}
				parts = append(parts, part{e, true})
				buf.Reset()
				inExpr = false
			} else {
				buf.WriteRune(ch)
			}
		default:
			buf.WriteRune(ch)
		}
	}
	if inExpr {
		if quoteCh != 0 {
			return Result{}, fmt.Errorf("unterminated string inside '{...}'")
		}
		return Result{}, fmt.Errorf("unclosed '{'")
	}
	if buf.Len() > 0 {
		parts = append(parts, part{buf.String(), false})
	}
	if len(parts) == 0 {
		return Result{}, fmt.Errorf("empty pattern")
	}
	if len(parts) == 1 {
		if !parts[0].expr {
			return Result{SQL: quote(parts[0].text), Kind: Expr}, nil
		}
		switch strings.ToUpper(parts[0].text) {
		case "NULL":
			return Result{SQL: "NULL", Kind: Null}, nil
		case "KEEP":
			return Result{SQL: "", Kind: Keep}, nil
		}
		return Result{SQL: parts[0].text, Kind: Expr}, nil
	}
	items := make([]string, len(parts))
	for i, pt := range parts {
		if pt.expr {
			items[i] = pt.text
		} else {
			items[i] = quote(pt.text)
		}
	}
	return Result{SQL: "CONCAT(" + strings.Join(items, ", ") + ")", Kind: Expr}, nil
}
