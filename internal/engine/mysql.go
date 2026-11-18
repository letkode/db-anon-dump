package engine

import (
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql" // registers the "mysql" database/sql driver
)

// MySQL implements Dialect for MySQL 8.
type MySQL struct{}

func (MySQL) Name() string         { return "mysql" }
func (MySQL) DefaultImage() string { return "mysql:8.0.32" }

func (MySQL) QuoteIdent(n string) string { return "`" + strings.ReplaceAll(n, "`", "``") + "`" }

func (MySQL) QuoteString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}

func (m MySQL) NotSame(col, expr string) string { return fmt.Sprintf("NOT (%s <=> %s)", col, expr) }

func (m MySQL) NullCase(col, expr string) string {
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE %s END", col, expr)
}

func (m MySQL) NotNullDefault(dt string) string {
	switch strings.ToLower(dt) {
	case "json":
		return "CAST('{}' AS JSON)"
	case "int", "bigint", "smallint", "tinyint", "mediumint", "decimal", "double", "float":
		return "0"
	case "date":
		return "'1970-01-01'"
	case "datetime", "timestamp":
		return "'1970-01-01 00:00:00'"
	case "time":
		return "'00:00:00'"
	}
	return "''"
}

func (m MySQL) ParkValue(idCol string) string { return "CONCAT('t-', " + m.QuoteIdent(idCol) + ")" }

func (MySQL) CharLength(expr string) string { return "CHAR_LENGTH(" + expr + ")" }

func (MySQL) RelaxConstraints() string { return "SET SESSION FOREIGN_KEY_CHECKS = 0" }

func (m MySQL) DropTableSQL(name string) string { return "DROP TABLE " + m.QuoteIdent(name) }

func (MySQL) DropFunctionSQL(name string) string { return "DROP FUNCTION IF EXISTS " + name }

func (MySQL) IsString(dt string) bool {
	dt = strings.ToLower(dt)
	return dt == "varchar" || dt == "char"
}

func (MySQL) IsFreeText(dt string) bool {
	switch strings.ToLower(dt) {
	case "json", "longtext", "mediumtext", "text", "tinytext":
		return true
	}
	return false
}

func (m MySQL) ListTablesSQL(db string) string {
	return "SELECT table_name FROM information_schema.tables WHERE table_schema = " + m.QuoteString(db) +
		" AND table_type = 'BASE TABLE' ORDER BY table_name"
}

func (m MySQL) ColumnsSQL(db string, tables []string) string {
	qs := make([]string, len(tables))
	for i, t := range tables {
		qs[i] = m.QuoteString(t)
	}
	return `SELECT c.table_name, c.column_name, c.is_nullable, c.data_type,
       COALESCE(c.character_maximum_length, 0),
       (SELECT COUNT(*) > 0 FROM information_schema.statistics s
         WHERE s.table_schema = c.table_schema AND s.table_name = c.table_name
           AND s.column_name = c.column_name AND s.non_unique = 0)
  FROM information_schema.columns c
 WHERE c.table_schema = ` + m.QuoteString(db) + ` AND c.table_name IN (` + strings.Join(qs, ", ") + `)
 ORDER BY c.table_name, c.ordinal_position`
}

func (MySQL) DriverName() string { return "mysql" }

func (MySQL) DSN(host string, port int, user, password, database string) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&multiStatements=false&timeout=10s",
		user, password, host, port, database)
}

func (MySQL) ContainerPort() int { return 3306 }

func (MySQL) AdminUser() string { return "root" }

func (MySQL) ContainerEnv(password, database string) []string {
	return []string{"MYSQL_ROOT_PASSWORD=" + password, "MYSQL_DATABASE=" + database}
}

// ContainerArgs: a throwaway database does not need durability, so the flush/doublewrite costs are off.
func (MySQL) ContainerArgs() []string {
	return []string{
		"--character-set-server=utf8mb4", "--collation-server=utf8mb4_unicode_ci",
		"--log-bin-trust-function-creators=1", "--skip-log-bin",
		"--innodb-flush-log-at-trx-commit=0", "--innodb-doublewrite=OFF", "--skip-innodb-adaptive-hash-index",
	}
}

func (MySQL) DataDir() string { return "/var/lib/mysql" }

func (MySQL) SecretEnv(password string) string { return "MYSQL_PWD=" + password }

func (MySQL) ClientCmd(database string) []string {
	return []string{"mysql", "-uroot", "--default-character-set=utf8mb4", database}
}

func (MySQL) SourceDumpCmd(host string, port int, user, database string, tables []string, mode DumpMode) []string {
	args := []string{"mysqldump", "-h" + host, fmt.Sprintf("-P%d", port), "-u" + user,
		"--single-transaction", "--quick", "--no-tablespaces", "--skip-triggers", "--set-gtid-purged=OFF",
		"--column-statistics=0", "--default-character-set=utf8mb4"}
	if mode == DumpSchema {
		args = append(args, "--no-data")
	} else {
		args = append(args, "--no-create-info")
	}
	args = append(args, database)
	return append(args, tables...)
}

func (MySQL) LocalDumpCmd(database string, tables []string) []string {
	args := []string{"mysqldump", "-uroot", "--single-transaction", "--no-tablespaces", "--skip-triggers",
		"--set-gtid-purged=OFF", "--column-statistics=0", "--default-character-set=utf8mb4", database}
	return append(args, tables...)
}
