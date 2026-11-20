package engine

import (
	"fmt"
	"regexp"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Postgres implements Dialect for PostgreSQL (schema "public").
//
// Patterns are PostgreSQL SQL: use MOD(id, 25) or id % 25, make_date(...), etc. CONCAT() accepts any type.
// Limitation: pg_dump -t does not follow dependencies, so a table whose foreign keys reference a table that
// is NOT extracted cannot be restored into the temporary database; extract the referenced tables too.
type Postgres struct{}

func (Postgres) Name() string         { return "postgres" }
func (Postgres) DefaultImage() string { return "postgres:16" }

func (Postgres) QuoteIdent(n string) string { return `"` + strings.ReplaceAll(n, `"`, `""`) + `"` }

// QuoteString: with standard_conforming_strings (the default) a backslash is an ordinary character.
func (Postgres) QuoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// NotSame compares as text so that json (which has no equality operator) and dates work too.
func (Postgres) NotSame(col, expr string) string {
	return fmt.Sprintf("(%s)::text IS DISTINCT FROM (%s)::text", col, expr)
}

func (Postgres) NullCase(col, expr string) string {
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE %s END", col, expr)
}

func (Postgres) NotNullDefault(dt string) string {
	switch strings.ToLower(dt) {
	case "json":
		return "'{}'::json"
	case "jsonb":
		return "'{}'::jsonb"
	case "integer", "bigint", "smallint", "numeric", "double precision", "real":
		return "0"
	case "boolean":
		return "false"
	case "date":
		return "'1970-01-01'::date"
	case "timestamp without time zone", "timestamp with time zone":
		return "'1970-01-01 00:00:00'"
	case "time without time zone", "time with time zone":
		return "'00:00:00'"
	case "uuid":
		return "'00000000-0000-0000-0000-000000000000'::uuid"
	}
	return "''"
}

func (p Postgres) ParkValue(idCol string) string { return "CONCAT('t-', " + p.QuoteIdent(idCol) + ")" }

func (Postgres) CharLength(expr string) string { return "CHAR_LENGTH((" + expr + ")::text)" }

// RelaxConstraints needs a superuser (the temporary database always has one).
func (Postgres) RelaxConstraints() string { return "SET session_replication_role = replica" }

func (p Postgres) DropTableSQL(name string) string {
	return "DROP TABLE " + p.QuoteIdent(name) + " CASCADE"
}

// DropFunctionSQL fails if the name has several overloads; use CREATE OR REPLACE-style unique names.
func (Postgres) DropFunctionSQL(name string) string { return "DROP FUNCTION IF EXISTS " + name }

func (Postgres) IsString(dt string) bool {
	dt = strings.ToLower(dt)
	return dt == "character varying" || dt == "character"
}

var postgresFreeText = []string{"text", "json", "jsonb", "xml"}

func (Postgres) FreeTextTypes() []string { return postgresFreeText }

func (Postgres) IsFreeText(dt string) bool {
	dt = strings.ToLower(dt)
	for _, t := range postgresFreeText {
		if t == dt {
			return true
		}
	}
	return false
}

// pg_dump writes "CREATE TABLE public.t (" (structure) and "COPY public.t (cols) FROM stdin;" (data).
var postgresMarker = regexp.MustCompile(`(?m)^(?:CREATE TABLE|COPY) (?:public\.)?"?([A-Za-z0-9_]+)"?`)

func (Postgres) StreamMarker() *regexp.Regexp { return postgresMarker }

func (p Postgres) TableSizesSQL(_ string, tables []string) string {
	qs := make([]string, len(tables))
	for i, t := range tables {
		qs[i] = p.QuoteString(t)
	}
	return "SELECT c.relname::text, pg_total_relation_size(c.oid)::bigint FROM pg_class c " +
		"JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relkind = 'r' " +
		"AND c.relname IN (" + strings.Join(qs, ", ") + ")"
}

func (p Postgres) ListTablesSQL(string) string {
	return "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' " +
		"AND table_type = 'BASE TABLE' ORDER BY table_name"
}

func (p Postgres) ColumnsSQL(_ string, tables []string) string {
	qs := make([]string, len(tables))
	for i, t := range tables {
		qs[i] = p.QuoteString(t)
	}
	return `SELECT c.table_name::text, c.column_name::text, c.is_nullable::text, c.data_type::text,
       COALESCE(c.character_maximum_length, 0)::bigint,
       CASE WHEN EXISTS (
            SELECT 1 FROM pg_index i
              JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
             WHERE i.indrelid = ('"' || c.table_schema || '"."' || c.table_name || '"')::regclass
               AND a.attname = c.column_name AND i.indisunique) THEN 1 ELSE 0 END::bigint
  FROM information_schema.columns c
 WHERE c.table_schema = 'public' AND c.table_name IN (` + strings.Join(qs, ", ") + `)
 ORDER BY c.table_name, c.ordinal_position`
}

func (Postgres) DriverName() string { return "pgx" }

func (Postgres) DSN(host string, port int, user, password, database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable&connect_timeout=10",
		user, password, host, port, database)
}

func (Postgres) ContainerPort() int { return 5432 }

func (Postgres) AdminUser() string { return "postgres" }

func (Postgres) ContainerEnv(password, database string) []string {
	return []string{"POSTGRES_PASSWORD=" + password, "POSTGRES_DB=" + database}
}

// ContainerArgs: a throwaway database does not need durability.
func (Postgres) ContainerArgs() []string {
	return []string{"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"}
}

func (Postgres) DataDir() string { return "/var/lib/postgresql/data" }

func (Postgres) SecretEnv(password string) string { return "PGPASSWORD=" + password }

func (Postgres) ClientCmd(database string) []string {
	return []string{"psql", "-U", "postgres", "-d", database, "-X", "-q", "-v", "ON_ERROR_STOP=1"}
}

func (Postgres) SourceDumpCmd(host string, port int, user, database string, tables []string, mode DumpMode) []string {
	args := []string{"pg_dump", "-h", host, "-p", fmt.Sprint(port), "-U", user, "-d", database,
		"--no-owner", "--no-privileges"}
	if mode == DumpSchema {
		args = append(args, "--schema-only")
	} else {
		args = append(args, "--data-only", "--disable-triggers")
	}
	for _, t := range tables {
		args = append(args, "-t", "public."+t)
	}
	return args
}

func (Postgres) LocalDumpCmd(database string, tables []string) []string {
	args := []string{"pg_dump", "-U", "postgres", "-d", database, "--no-owner", "--no-privileges"}
	for _, t := range tables {
		args = append(args, "-t", "public."+t)
	}
	return args
}
