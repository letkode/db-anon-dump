// Package engine isolates everything that differs between SQL engines. One config file declares ONE
// engine ("engine: mysql"); its patterns are written in that engine's own SQL. Adding an engine means
// implementing Dialect; nothing else in the program knows the SQL flavour.
package engine

import (
	"fmt"
	"strings"
)

// Column is one column of an extracted table, as read from the temporary database.
type Column struct {
	Table    string
	Name     string
	Nullable bool
	DataType string
	CharMax  int // maximum length in characters for string types, 0 otherwise
	Unique   bool
}

// DumpMode selects which part of a table is dumped from the source.
type DumpMode int

const (
	DumpSchema DumpMode = iota // structure only
	DumpData                   // rows only
)

// Dialect is the contract an SQL engine must fulfil.
type Dialect interface {
	Name() string
	DefaultImage() string

	// --- SQL generation ---
	QuoteIdent(name string) string
	QuoteString(s string) string
	NotSame(col, expr string) string // true when col differs from expr (NULL-safe)
	NullCase(col, expr string) string // expr, but NULL stays NULL
	NotNullDefault(dataType string) string
	ParkValue(idCol string) string // a unique placeholder derived from the id column
	CharLength(expr string) string
	RelaxConstraints() string // statement that disables foreign keys for the session
	DropTableSQL(name string) string
	DropFunctionSQL(name string) string // name is validated as a plain identifier by the config
	IsString(dataType string) bool   // fixed/variable length character types (parkable, length-checked)
	IsFreeText(dataType string) bool // text/JSON types: audited because they may hold anything

	// --- introspection ---
	ListTablesSQL(database string) string
	ColumnsSQL(database string, tables []string) string

	// --- connection ---
	DriverName() string
	DSN(host string, port int, user, password, database string) string

	// --- temporary container ---
	ContainerPort() int
	AdminUser() string // superuser of the temporary database
	ContainerEnv(password, database string) []string
	ContainerArgs() []string
	DataDir() string
	SecretEnv(password string) string
	ClientCmd(database string) []string
	SourceDumpCmd(host string, port int, user, database string, tables []string, mode DumpMode) []string
	LocalDumpCmd(database string, tables []string) []string
}

// Get returns the dialect for the engine name used in the config file.
func Get(name string) (Dialect, error) {
	switch strings.ToLower(name) {
	case "mysql":
		return MySQL{}, nil
	case "mariadb":
		return MariaDB{}, nil
	case "postgres", "postgresql":
		return Postgres{}, nil
	case "sqlserver", "mssql", "oracle":
		return nil, fmt.Errorf("engine %q is planned but not implemented yet: it has no mysqldump/pg_dump equivalent, so it needs a native exporter (see README, Roadmap)", name)
	case "":
		return nil, fmt.Errorf("engine is not set (supported: %s)", strings.Join(Names(), ", "))
	default:
		return nil, fmt.Errorf("unknown engine %q (supported: %s)", name, strings.Join(Names(), ", "))
	}
}

// Names lists the supported engines.
func Names() []string { return []string{"mysql", "mariadb", "postgres"} }
