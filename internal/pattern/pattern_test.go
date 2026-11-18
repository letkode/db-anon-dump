package pattern

import (
	"strings"
	"testing"
)

func q(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		sql  string
		kind Kind
	}{
		{"user-{id}@example.test", "CONCAT('user-', id, '@example.test')", Expr},
		{"Nombre{id}", "CONCAT('Nombre', id)", Expr},
		{"{id}", "id", Expr},
		{"plain text", "'plain text'", Expr},
		{"it's", "'it''s'", Expr},
		{`a\b`, `'a\\b'`, Expr},
		{"{NULL}", "NULL", Null},
		{"{null}", "NULL", Null},
		{"{KEEP}", "", Keep},
		{"+56900{LPAD(id MOD 1000000, 6, '0')}", "CONCAT('+56900', LPAD(id MOD 1000000, 6, '0'))", Expr},
		{"{MAKEDATE(YEAR(birthdate), 1)}", "MAKEDATE(YEAR(birthdate), 1)", Expr},
		// braces inside SQL strings and regex quantifiers are not pattern braces
		{`{REGEXP_REPLACE(c, '[a-z]{2,}', '{x}')}`, `REGEXP_REPLACE(c, '[a-z]{2,}', '{x}')`, Expr},
		{`{REGEXP_REPLACE(c, '\\.[a-z]{2,}', 'y')}`, `REGEXP_REPLACE(c, '\\.[a-z]{2,}', 'y')`, Expr},
		{"{IF(id MOD 2 = 0, 'M', 'F')}", "IF(id MOD 2 = 0, 'M', 'F')", Expr},
		{"x{ {1}+1 }y", "CONCAT('x', {1}+1, 'y')", Expr}, // nested braces are kept
		{"192.0.2.{(id MOD 254) + 1}", "CONCAT('192.0.2.', (id MOD 254) + 1)", Expr},
		{"{'{}'}", "'{}'", Expr},
	}
	for _, c := range cases {
		got, err := Parse(c.in, q)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", c.in, err)
			continue
		}
		if got.SQL != c.sql || got.Kind != c.kind {
			t.Errorf("Parse(%q) = (%q, %v), want (%q, %v)", c.in, got.SQL, got.Kind, c.sql, c.kind)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "{", "abc{id", "{}", "{  }", "a}b", "{'unterminated}", `{"x}`} {
		if _, err := Parse(in, q); err == nil {
			t.Errorf("Parse(%q): expected an error", in)
		}
	}
}
