#!/usr/bin/env bash
#
# test/e2e-engine.sh <mysql|mariadb|postgres> -- end-to-end test of dbanondump for one engine (needs Docker).
#
#   1. starts a "source" database of that engine with a small schema and real-looking FAKE data
#      (testdata/generic/). No real data is involved at any point.
#   2. extracts from the live source, anonymises, writes the dump
#   3. RESTORES the dump into a fresh database and checks its content
#   4. repeats from a dump file and exercises the failure modes
#
# Usage:  test/e2e-engine.sh postgres      (make e2e-postgres / e2e-mariadb / e2e-mysql)
set -euo pipefail

ENGINE="${1:?usage: $0 <mysql|mariadb|postgres>}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${DBANONDUMP_BIN:-$ROOT/dist/dbanondump-linux-amd64}"
WORK="$(mktemp -d)"
SRC="dbanondump-e2e-$ENGINE-$$"
PW="fixturepw"
FAILURES=0

[[ -x "$BIN" ]] || { echo "binary not found: $BIN (run: make build)"; exit 2; }

case "$ENGINE" in
    mysql)
        IMAGE="mysql:8.0.32"; INPORT=3306; DBUSER=root; SQLFILE="$ROOT/testdata/generic/mysql.sql"
        ENVS=(-e MYSQL_ROOT_PASSWORD="$PW" -e MYSQL_DATABASE=fixtures)
        ARGS=(--character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci --innodb-flush-log-at-trx-commit=0 --innodb-doublewrite=OFF)
        NONDET='n{FLOOR(RAND() * 1000000000)}'
        FN_SQL="CREATE FUNCTION fake_tag(p BIGINT) RETURNS VARCHAR(20) DETERMINISTIC NO SQL RETURN CONCAT('tag-', p)"
        sql()     { docker exec -i -e MYSQL_PWD="$PW" "$SRC" mysql -uroot --default-character-set=utf8mb4 "$@"; }
        sqlq()    { sql fixtures -N -B -e "$1"; }
        sqlr()    { sql restored -N -B -e "$1"; }
        newdb()   { sql -e "CREATE DATABASE restored"; }
        loadfile(){ sql "$1" < "$2"; }
        srcdump() { docker exec -e MYSQL_PWD="$PW" "$SRC" mysqldump -uroot --single-transaction --no-tablespaces --skip-triggers fixtures; } ;;
    mariadb)
        IMAGE="mariadb:10.11"; INPORT=3306; DBUSER=root; SQLFILE="$ROOT/testdata/generic/mysql.sql"
        ENVS=(-e MARIADB_ROOT_PASSWORD="$PW" -e MARIADB_DATABASE=fixtures)
        ARGS=(--character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci)
        NONDET='n{FLOOR(RAND() * 1000000000)}'
        FN_SQL="CREATE FUNCTION fake_tag(p BIGINT) RETURNS VARCHAR(20) DETERMINISTIC NO SQL RETURN CONCAT('tag-', p)"
        sql()     { docker exec -i -e MYSQL_PWD="$PW" "$SRC" mariadb -uroot --default-character-set=utf8mb4 "$@"; }
        sqlq()    { sql fixtures -N -B -e "$1"; }
        sqlr()    { sql restored -N -B -e "$1"; }
        newdb()   { sql -e "CREATE DATABASE restored"; }
        loadfile(){ sql "$1" < "$2"; }
        srcdump() { docker exec -e MYSQL_PWD="$PW" "$SRC" mariadb-dump -uroot --single-transaction --skip-triggers fixtures; } ;;
    postgres)
        IMAGE="postgres:16"; INPORT=5432; DBUSER=postgres; SQLFILE="$ROOT/testdata/generic/postgres.sql"
        ENVS=(-e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=fixtures)
        ARGS=(-c fsync=off -c synchronous_commit=off)
        NONDET='n{FLOOR(RANDOM() * 1000000000)}'
        FN_SQL="CREATE FUNCTION fake_tag(p BIGINT) RETURNS TEXT LANGUAGE sql IMMUTABLE AS \$\$ SELECT 'tag-' || p \$\$"
        sql()     { docker exec -i -e PGPASSWORD="$PW" "$SRC" psql -U postgres -X -q -v ON_ERROR_STOP=1 "$@"; }
        sqlq()    { sql -d fixtures -At -c "$1"; }
        sqlr()    { sql -d restored -At -c "$1"; }
        newdb()   { sql -d postgres -c "CREATE DATABASE restored"; }
        loadfile(){ sql -d "$1" < "$2"; }
        srcdump() { docker exec -e PGPASSWORD="$PW" "$SRC" pg_dump -U postgres -d fixtures --no-owner --no-privileges; } ;;
    *) echo "unknown engine: $ENGINE"; exit 2 ;;
esac

cleanup() { docker rm -f -v "$SRC" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

say()  { printf '\n== %s\n' "$*"; }
pass() { printf '   PASS  %s\n' "$*"; }
fail() { printf '   FAIL  %s\n' "$*"; FAILURES=$((FAILURES + 1)); }

expect_exit() {   # expect_exit <name> <expected code> <command...>
    local name="$1" want="$2"; shift 2
    set +e; "$@" >"$WORK/last.log" 2>&1; local got=$?; set -e
    if [[ $got -eq $want ]]; then pass "$name (exit $got)"; else fail "$name: expected exit $want, got $got"; sed 's/^/        | /' "$WORK/last.log" | tail -15; fi
}

say "[$ENGINE] building the source database ($IMAGE)"
docker run -d --name "$SRC" -p 0.0.0.0::$INPORT "${ENVS[@]}" "$IMAGE" "${ARGS[@]}" >/dev/null
for _ in $(seq 1 90); do
    sleep 2
    if sqlq "SELECT 1" >/dev/null 2>&1; then
        sleep 3
        sqlq "SELECT 1" >/dev/null 2>&1 && break
    fi
done
if [[ "$ENGINE" == postgres ]]; then sql -d fixtures < "$SQLFILE" >/dev/null; else sql fixtures < "$SQLFILE"; fi
PORT="$(docker port "$SRC" "$INPORT/tcp" | head -1 | sed 's/.*://')"
echo "source database on port $PORT"
export DBANONDUMP_SOURCE_PASSWORD="$PW"

mkdir -p "$WORK/main"
cp "$ROOT/testdata/generic/$ENGINE/columns.yaml" "$WORK/main/columns.yaml"
sed -e "s/__HOST__/127.0.0.1/" -e "s/__PORT__/$PORT/" "$ROOT/testdata/generic/$ENGINE/config.yaml" > "$WORK/main/config.yaml"

cfg() {   # cfg <name> <columns.yaml content>   -- a small project over country + app_user
    mkdir -p "$WORK/$1"
    cat > "$WORK/$1/config.yaml" <<YAML
version: 1
engine: $ENGINE
source: {host: 127.0.0.1, port: $PORT, user: $DBUSER, database: fixtures, container_host: host.docker.internal}
tables: {include: ["country", "app_user"]}
YAML
    printf '%s\n' "$2" > "$WORK/$1/columns.yaml"
}

say "[$ENGINE] validate (offline)"
expect_exit "the config is valid" 0 "$BIN" validate -c "$WORK/main/config.yaml"

say "[$ENGINE] live source: structure -> pre-flight -> data -> anonymise -> dump"
expect_exit "extract + anonymise from a live source" 0 "$BIN" run -c "$WORK/main/config.yaml" -o "$WORK/out.sql"

say "[$ENGINE] the password is read from .env next to config.yaml (nothing exported)"
mkdir -p "$WORK/dotenv"
cp "$WORK/main/config.yaml" "$WORK/main/columns.yaml" "$WORK/dotenv/"
printf "DBANONDUMP_SOURCE_PASSWORD='%s'\n" "$PW" > "$WORK/dotenv/.env"
expect_exit "run with the password only in .env" 0 env -u DBANONDUMP_SOURCE_PASSWORD "$BIN" run -c "$WORK/dotenv/config.yaml" -o "$WORK/out-dotenv.sql"
rm -f "$WORK/dotenv/.env"
expect_exit "without .env and without the variable it is a usage error" 2 env -u DBANONDUMP_SOURCE_PASSWORD "$BIN" run -c "$WORK/dotenv/config.yaml" -o "$WORK/out-dotenv2.sql"

say "[$ENGINE] run log: logs/<command>-<datetime>.log"
LOGF="$(ls -t "$WORK"/main/logs/run-*.log 2>/dev/null | head -1)"
if [[ -n "$LOGF" ]]; then pass "a run log was written in logs/ next to config.yaml"; else fail "no run log in $WORK/main/logs"; LOGF=/dev/null; fi
grep -q 'result:   OK (exit 0)' "$LOGF" && grep -q '^duration: ' "$LOGF" && grep -q 'anonymise' "$LOGF" \
    && pass "the log has the result, the total duration and the time per phase" || fail "the log misses the summary"
grep -q "$PW" "$LOGF" && fail "the password is in the log" || pass "the password is not in the log"
[[ "$(stat -c %a "$LOGF" 2>/dev/null)" == 600 ]] && pass "the log is mode 600" || fail "the log is not mode 600"
ERRF="$(ls -t "$WORK"/dotenv/logs/run-*.log 2>/dev/null | head -1)"
grep -q 'result:   ERROR (exit 2)' "${ERRF:-/dev/null}" && grep -q 'ERROR:' "$ERRF" \
    && pass "a failed run logs the error and the exit code" || fail "the failed run is not in its log"
expect_exit "--no-log writes no log" 0 "$BIN" run -c "$WORK/main/config.yaml" -o "$WORK/out-nolog.sql" --no-log
[[ "$(ls "$WORK"/main/logs/run-*.log | wc -l)" -eq 1 ]] && pass "--no-log left no new file" || fail "--no-log still wrote a log"

say "[$ENGINE] progress, --verbose, an empty columns.yaml and --suggest"
expect_exit "run with progress" 0 "$BIN" run -c "$WORK/main/config.yaml" -o "$WORK/out-progress.sql" --no-log
grep -q 'structure \[1/' "$WORK/last.log" && grep -q 'data \[1/' "$WORK/last.log" && grep -q 'ok  \[1/' "$WORK/last.log" \
    && pass "run shows the table being copied and the anonymise counter" || fail "run printed no progress lines"
expect_exit "check --verbose" 0 "$BIN" check -c "$WORK/main/config.yaml" --verbose --no-log
grep -q 'temporary database ready after' "$WORK/last.log" && pass "--verbose adds detail" || fail "--verbose printed nothing extra"
cfg sug ""
expect_exit "validate accepts an empty columns.yaml" 0 "$BIN" validate -c "$WORK/sug/config.yaml"
grep -q 'WARNING' "$WORK/last.log" && pass "...and warns about it" || fail "no warning for the empty columns.yaml"
expect_exit "run refuses an empty columns.yaml without --allow-empty" 3 "$BIN" run -c "$WORK/sug/config.yaml" -o "$WORK/out-empty.sql" --no-log
expect_exit "run --allow-empty goes ahead" 0 "$BIN" run -c "$WORK/sug/config.yaml" -o "$WORK/out-empty.sql" --no-log --allow-empty
expect_exit "check --suggest" 0 "$BIN" check -c "$WORK/sug/config.yaml" --suggest --no-log
SUG="$WORK/sug/columns.suggested.yaml"
[[ -f "$SUG" ]] && pass "columns.suggested.yaml was written" || fail "columns.suggested.yaml is missing"
if [[ -f "$SUG" ]] && grep -v -E '^(#|$)' "$SUG" | grep -q .; then fail "the suggestions are not all commented out"; else pass "every suggested line is commented out"; fi
mkdir -p "$WORK/sug2"; cp "$WORK/sug/config.yaml" "$WORK/sug2/config.yaml"
grep -v '^##' "$SUG" | sed -E 's/^# //' > "$WORK/sug2/columns.yaml"
expect_exit "the uncommented suggestions pass the pre-flight on this engine" 0 "$BIN" check -c "$WORK/sug2/config.yaml" --no-log
expect_exit "the uncommented suggestions also RUN (every pattern repeats on the anonymised rows)" 0 "$BIN" run -c "$WORK/sug2/config.yaml" -o "$WORK/out-sug2.sql" --no-log
OUT="$WORK/out.sql"
if [[ -f "$OUT" ]]; then
    grep -q 'user-1@example.test' "$OUT"          && pass "dump contains the synthetic e-mail"       || fail "no synthetic e-mail"
    ! grep -q 'maria.gonzalez@gmail.com' "$OUT"   && pass "real e-mail is gone"                      || fail "real e-mail survived"
    ! grep -qi 'Providencia' "$OUT"               && pass "free text (bio) is gone"                   || fail "free text survived"
    ! grep -q 'Maria Gonzalez' "$OUT"             && pass "real name is gone"                         || fail "real name survived"
    ! grep -q 'mail_log' "$OUT"                   && pass "excluded table not extracted"              || fail "mail_log was extracted"
    grep -q 'REALHASH-A' "$OUT"                   && pass "a column without pattern is copied as is"  || fail "unlisted column was altered"
fi

say "[$ENGINE] the dump restores into a fresh database"
newdb >/dev/null 2>&1 || true
loadfile restored "$OUT" >"$WORK/restore.log" 2>&1 && pass "dump restored without errors" || { fail "dump restore failed"; tail -8 "$WORK/restore.log"; }
[[ "$(sqlr "SELECT email FROM app_user WHERE id = 1" 2>/dev/null)" == "user-1@example.test" ]] && pass "restored e-mail is anonymised" || fail "restored e-mail wrong"
[[ "$(sqlr "SELECT COUNT(*) FROM profile" 2>/dev/null)" == "3" ]] && pass "restored profile rows (3)" || fail "restored profile row count wrong"
[[ "$(sqlr "SELECT name FROM country WHERE id = 1" 2>/dev/null)" == "Chile" ]] && pass "untouched table kept intact" || fail "untouched table altered"
[[ "$(sqlr "SELECT COUNT(*) FROM app_user WHERE phone IS NULL" 2>/dev/null)" == "1" ]] && pass "NULL phone stays NULL" || fail "NULL was not preserved"

say "[$ENGINE] default output name carries the date and time"
expect_exit "default output (no -o)" 0 "$BIN" run -c "$WORK/main/config.yaml" -t country,app_user
DATED="$(ls "$WORK"/main/output/dev-dump-[0-9]*-[0-9]*.sql.gz 2>/dev/null | grep -v -- '-schema' | head -1)"
[[ -n "$DATED" && -f "$DATED" ]] && gzip -t "$DATED" && pass "the default dump name carries the date and time ($(basename "$DATED"))" || fail "no dump named dev-dump-<date>-<time>.sql.gz in $WORK/main/output"

say "[$ENGINE] schema_only: structure without rows"
has_rows() {   # has_rows <dump file> <table regex>: true when the dump holds at least one row of the table(s)
    grep -q -E "INSERT INTO .?$2" "$1" && return 0
    # pg_dump writes an empty "COPY ... FROM stdin;" + "\." for empty tables: only a line inside the block is a row
    awk -v t="$2" '$0 ~ "^COPY public\\." t " " {inblk=1; next} inblk && $0 == "\\." {inblk=0; next} inblk {found=1} END {exit !found}' "$1"
}
expect_exit "--schema-only on one table" 0 "$BIN" run -c "$WORK/main/config.yaml" -t country,app_user --schema-only app_user -o "$WORK/out-so.sql" --no-log
grep -q 'CREATE TABLE.*app_user' "$WORK/out-so.sql" && pass "the schema_only table keeps its CREATE TABLE" || fail "app_user lost its structure"
has_rows "$WORK/out-so.sql" app_user && fail "app_user still has rows" || pass "the schema_only table has no rows"
has_rows "$WORK/out-so.sql" country && pass "the other tables keep their rows" || fail "country lost its rows"
expect_exit "--schema-only on a dump import (--from-dump)" 0 "$BIN" run -c "$WORK/main/config.yaml" --from-dump "$WORK/out.sql" -t country,app_user --schema-only app_user -o "$WORK/out-so2.sql" --no-log
has_rows "$WORK/out-so2.sql" app_user && fail "app_user kept its rows after --from-dump" || pass "--from-dump: the schema_only table was emptied"
has_rows "$WORK/out-so2.sql" country && pass "--from-dump: the other tables keep their rows" || fail "--from-dump: country lost its rows"
expect_exit "check leaves schema_only tables out of the audit" 0 "$BIN" check -c "$WORK/main/config.yaml" --schema-only app_user --no-log
if grep -q 'app_user\.' "$WORK/last.log"; then fail "the audit still lists columns of a schema_only table"; else pass "the audit does not list the schema_only table"; fi
grep -q 'country\.name' "$WORK/last.log" && pass "...and still lists the tables that do carry data" || fail "the audit lost the other tables (country.name has no pattern)"
expect_exit "a schema_only spec that matches nothing" 0 "$BIN" run -c "$WORK/main/config.yaml" -t country --schema-only 'nothere_*' -o "$WORK/out-warn.sql" --no-log
grep -q 'WARNING: schema_only "nothere_\*" matches no extracted table' "$WORK/last.log" && pass "...it warns instead of staying silent" || fail "no warning for the unmatched schema_only spec"
cfg so ""
expect_exit "'*' dumps only the structure, with an empty columns.yaml and no --allow-empty" 0 "$BIN" run -c "$WORK/so/config.yaml" --schema-only '*' --no-log
SO="$(ls "$WORK"/so/output/dev-dump-*-schema.sql.gz 2>/dev/null | head -1)"
[[ -n "$SO" ]] && pass "the file name carries -schema ($(basename "$SO"))" || fail "no *-schema.sql.gz in $WORK/so/output"
[[ -n "$SO" ]] && gzip -dc "$SO" > "$WORK/so.sql"
if [[ -f "$WORK/so.sql" ]] && has_rows "$WORK/so.sql" '.*'; then fail "the structure-only dump has rows"; else pass "the structure-only dump has no rows"; fi
[[ -n "$SO" ]] && gzip -dc "$SO" | grep -q 'CREATE TABLE.*app_user' && pass "...and has the CREATE TABLEs" || fail "the structure-only dump has no CREATE TABLE"

say "[$ENGINE] '*' wildcard and functions.yaml"
mkdir -p "$WORK/wild"
cat > "$WORK/wild/config.yaml" <<YAML
version: 1
engine: $ENGINE
source: {host: 127.0.0.1, port: $PORT, user: $DBUSER, database: fixtures, container_host: host.docker.internal}
tables: {include: ["country", "app_user"]}
YAML
cat > "$WORK/wild/columns.yaml" <<'YAML'
"*":
  email: "wild-{id}@example.test"
  phone: "{fake_tag(id)}"
app_user:
  email: "exact-{id}@example.test"
YAML
{ echo "- name: fake_tag"; echo "  sql: |"; echo "    $FN_SQL"; } > "$WORK/wild/functions.yaml"
expect_exit "wildcard + a function from functions.yaml" 0 "$BIN" run -c "$WORK/wild/config.yaml" -o "$WORK/out-wild.sql"
grep -q 'exact-1@example.test' "$WORK/out-wild.sql" 2>/dev/null && pass "an exact entry overrides the wildcard" || fail "exact entry did not win"
grep -q 'tag-1' "$WORK/out-wild.sql" 2>/dev/null && pass "the wildcard applied a pattern that calls the function" || fail "the function-based pattern was not applied"
! grep -q 'wild-1@example.test' "$WORK/out-wild.sql" 2>/dev/null && pass "the wildcard does not override the exact entry" || fail "wildcard overrode the exact entry"
mkdir -p "$WORK/badfn"; cp "$WORK/wild/config.yaml" "$WORK/wild/columns.yaml" "$WORK/badfn/"
printf -- "- name: broken\n  sql: THIS IS NOT SQL\n" > "$WORK/badfn/functions.yaml"
expect_exit "a function with invalid SQL aborts" 3 "$BIN" run -c "$WORK/badfn/config.yaml" -o "$WORK/out-badfn.sql"
[[ ! -e "$WORK/out-badfn.sql" ]] && pass "no dump written when a function fails" || fail "a dump was written although a function failed"

say "[$ENGINE] masking e-mails and phones inside free text (keeps the rest of the text)"
mkdir -p "$WORK/mask"; cp "$WORK/wild/config.yaml" "$WORK/mask/config.yaml"
if [[ "$ENGINE" == postgres ]]; then
cat > "$WORK/mask/columns.yaml" <<'YAML'
app_user:
  notes: |-
    {regexp_replace(regexp_replace(notes, '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}', '[email removed]', 'g'), '(\+?56)?[ ]?9[ ]?[0-9]{4}[ ]?[0-9]{4}', '[phone removed]', 'g')}
YAML
else
cat > "$WORK/mask/columns.yaml" <<'YAML'
app_user:
  notes: |-
    {REGEXP_REPLACE(REGEXP_REPLACE(notes, '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}', '[email removed]'), '(\\+?56)?[ ]?9[ ]?[0-9]{4}[ ]?[0-9]{4}', '[phone removed]')}
YAML
fi
expect_exit "regex masking pattern" 0 "$BIN" run -c "$WORK/mask/config.yaml" -o "$WORK/out-mask.sql"
! grep -q 'contacto.maria@correo.cl' "$WORK/out-mask.sql" 2>/dev/null && pass "the e-mail inside the text is masked" || fail "the e-mail survived"
! grep -q '1111 2222' "$WORK/out-mask.sql" 2>/dev/null && pass "the phone inside the text is masked" || fail "the phone survived"
grep -q '\[email removed\]' "$WORK/out-mask.sql" 2>/dev/null && grep -q '\[phone removed\]' "$WORK/out-mask.sql" 2>/dev/null && pass "both markers are present" || fail "markers missing"
grep -q 'despues de las 18' "$WORK/out-mask.sql" 2>/dev/null && pass "the rest of the text is kept" || fail "the surrounding text was lost"

say "[$ENGINE] dump file as the source"
srcdump > "$WORK/source-dump.sql"
mkdir -p "$WORK/dumpmode"
cp "$ROOT/testdata/generic/$ENGINE/columns.yaml" "$WORK/dumpmode/columns.yaml"
sed -e "s/__HOST__/unused/" -e "s/__PORT__/1/" "$ROOT/testdata/generic/$ENGINE/config.yaml" > "$WORK/dumpmode/config.yaml"
expect_exit "extract + anonymise from a dump file" 0 "$BIN" run -c "$WORK/dumpmode/config.yaml" --from-dump "$WORK/source-dump.sql" -o "$WORK/out-dump.sql"
grep -q 'user-1@example.test' "$WORK/out-dump.sql" 2>/dev/null && pass "dump mode anonymised app_user.email" || fail "dump mode output wrong"

say "[$ENGINE] failure modes (nothing may be written)"
cfg f-col 'app_user: {no_such_column: "{NULL}"}'
expect_exit "a column that does not exist" 3 "$BIN" run -c "$WORK/f-col/config.yaml" -o "$WORK/f-col.sql"
cfg f-fn 'app_user: {email: "{no_such_function(id)}"}'
expect_exit "invalid SQL is caught by the pre-flight" 3 "$BIN" run -c "$WORK/f-fn/config.yaml" -o "$WORK/f-fn.sql"
grep -q 'columns.app_user.email' "$WORK/last.log" && pass "the pre-flight names the column" || fail "message does not name the column"
cfg f-len "app_user: {username: \"x{REPEAT('a', 400)}\"}"
expect_exit "a pattern longer than the column is caught before the UPDATE" 3 "$BIN" run -c "$WORK/f-len/config.yaml" -o "$WORK/f-len.sql"
cfg f-nd "app_user: {phone: \"$NONDET\"}"
expect_exit "a non-deterministic pattern is caught by the check" 6 "$BIN" run -c "$WORK/f-nd/config.yaml" -o "$WORK/f-nd.sql"
cfg f-strict 'app_user: {email: "user-{id}@example.test"}'
expect_exit "--strict aborts on personal-looking columns without a pattern" 3 "$BIN" run -c "$WORK/f-strict/config.yaml" --strict -o "$WORK/f-strict.sql"
expect_exit "check lists them without failing" 0 "$BIN" check -c "$WORK/f-strict/config.yaml"
grep -q 'app_user.password' "$WORK/last.log" && pass "check output lists app_user.password" || fail "check output misses app_user.password"
expect_exit "a requested table that does not exist" 2 "$BIN" run -c "$WORK/f-strict/config.yaml" -t app_user,no_such_table -o "$WORK/f-tab.sql"
for f in f-col f-fn f-len f-nd f-strict f-tab; do
    [[ ! -e "$WORK/$f.sql" ]] || fail "a dump was written although $f failed"
done
pass "no dump was written by any failed run"

say "[$ENGINE] no container left behind"
if docker ps -a --format '{{.Names}}' | grep -q '^dbanondump-[0-9]'; then fail "temporary container left behind"; else pass "all temporary containers were removed"; fi

say "[$ENGINE] result"
if [[ $FAILURES -eq 0 ]]; then echo "all tests passed"; else echo "$FAILURES test(s) FAILED"; exit 1; fi
