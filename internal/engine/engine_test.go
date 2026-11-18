package engine

import "testing"

func TestGet(t *testing.T) {
	if d, err := Get("MySQL"); err != nil || d.Name() != "mysql" {
		t.Fatalf("Get(MySQL) = %v, %v", d, err)
	}
	for _, n := range []string{"mariadb", "postgres", "PostgreSQL"} {
		if _, err := Get(n); err != nil {
			t.Errorf("Get(%q): %v", n, err)
		}
	}
	for _, n := range []string{"oracle", "sqlserver", "mssql", "sqlite", ""} {
		if _, err := Get(n); err == nil {
			t.Errorf("Get(%q): expected an error", n)
		}
	}
}

func TestMySQLQuoting(t *testing.T) {
	m := MySQL{}
	if got := m.QuoteString(`a'b\c`); got != `'a''b\\c'` {
		t.Errorf("QuoteString = %s", got)
	}
	if got := m.QuoteIdent("we`ird"); got != "`we``ird`" {
		t.Errorf("QuoteIdent = %s", got)
	}
	if got := m.NotNullDefault("json"); got != "CAST('{}' AS JSON)" {
		t.Errorf("NotNullDefault(json) = %s", got)
	}
	if got := m.DropFunctionSQL("fake_rut"); got != "DROP FUNCTION IF EXISTS fake_rut" {
		t.Errorf("DropFunctionSQL = %s", got)
	}
}

func TestPostgres(t *testing.T) {
	p := Postgres{}
	if got := p.QuoteString(`a'b\c`); got != `'a''b\c'` { // a backslash is NOT doubled
		t.Errorf("QuoteString = %s", got)
	}
	if got := p.QuoteIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("QuoteIdent = %s", got)
	}
	if got := p.NotSame(`"c"`, "x"); got != `("c")::text IS DISTINCT FROM (x)::text` {
		t.Errorf("NotSame = %s", got)
	}
	if got := p.NotNullDefault("jsonb"); got != "'{}'::jsonb" {
		t.Errorf("NotNullDefault(jsonb) = %s", got)
	}
	if !p.IsString("character varying") || p.IsString("text") || !p.IsFreeText("jsonb") || p.IsFreeText("integer") {
		t.Error("type classification")
	}
	if cmd := p.SourceDumpCmd("h", 5432, "u", "db", []string{"a", "b"}, DumpData); cmd[0] != "pg_dump" ||
		!contains(cmd, "--data-only") || !contains(cmd, "public.b") {
		t.Errorf("SourceDumpCmd = %v", cmd)
	}
}

func TestMariaDBDiffersFromMySQLOnlyWhereItMust(t *testing.T) {
	m := MariaDB{}
	if m.Name() != "mariadb" || m.ClientCmd("db")[0] != "mariadb" || m.LocalDumpCmd("db", nil)[0] != "mariadb-dump" {
		t.Error("client binaries")
	}
	if got := m.QuoteString(`a\b`); got != `'a\\b'` { // inherited from MySQL: backslash is doubled
		t.Errorf("QuoteString = %s", got)
	}
	for _, a := range m.SourceDumpCmd("h", 3306, "u", "db", nil, DumpSchema) {
		if a == "--column-statistics=0" || a == "--no-tablespaces" || a == "--set-gtid-purged=OFF" {
			t.Errorf("MySQL-only dump option %s must not be passed to mariadb-dump", a)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
