package engine

import "fmt"

// MariaDB is MySQL-compatible for SQL and for the wire protocol (same Go driver); it differs in the
// container image, the client binaries and a few dump options. Note: JSON columns are LONGTEXT with a
// CHECK in MariaDB, so a NOT NULL JSON column needs an explicit valid pattern such as "{'{}'}".
type MariaDB struct{ MySQL }

func (MariaDB) Name() string         { return "mariadb" }
func (MariaDB) DefaultImage() string { return "mariadb:10.11" }

func (MariaDB) ContainerEnv(password, database string) []string {
	return []string{"MARIADB_ROOT_PASSWORD=" + password, "MARIADB_DATABASE=" + database}
}

func (MariaDB) ContainerArgs() []string {
	return []string{
		"--character-set-server=utf8mb4", "--collation-server=utf8mb4_unicode_ci",
		"--skip-log-bin", "--innodb-flush-log-at-trx-commit=0", "--innodb-doublewrite=OFF",
		"--skip-innodb-adaptive-hash-index",
	}
}

func (MariaDB) ClientCmd(database string) []string {
	return []string{"mariadb", "-uroot", "--default-character-set=utf8mb4", database}
}

func (MariaDB) SourceDumpCmd(host string, port int, user, database string, tables []string, mode DumpMode) []string {
	args := []string{"mariadb-dump", "-h" + host, fmt.Sprintf("-P%d", port), "-u" + user,
		"--single-transaction", "--quick", "--skip-triggers", "--default-character-set=utf8mb4"}
	if mode == DumpSchema {
		args = append(args, "--no-data")
	} else {
		args = append(args, "--no-create-info")
	}
	args = append(args, database)
	return append(args, tables...)
}

func (MariaDB) LocalDumpCmd(database string, tables []string) []string {
	args := []string{"mariadb-dump", "-uroot", "--single-transaction", "--skip-triggers",
		"--default-character-set=utf8mb4", database}
	return append(args, tables...)
}
