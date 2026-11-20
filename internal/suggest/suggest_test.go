package suggest

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/letkode/db-anon-dump/internal/engine"
	"github.com/letkode/db-anon-dump/internal/plan"
)

var sample = []plan.AuditCol{
	{Table: "user", Column: "email", DataType: "varchar", CharMax: 255, HasID: true},
	{Table: "user", Column: "phone", DataType: "varchar", CharMax: 15, HasID: true},
	{Table: "user", Column: "second_phone", DataType: "varchar", CharMax: 8, HasID: true},
	{Table: "user", Column: "password", DataType: "varchar", CharMax: 255, HasID: true},
	{Table: "user", Column: "birthdate", DataType: "date", HasID: true},
	{Table: "user", Column: "valid_email", DataType: "tinyint", HasID: true},
	{Table: "user", Column: "notes", DataType: "longtext", HasID: true},
	{Table: "user", Column: "name", DataType: "varchar", CharMax: 50, HasID: true},
	{Table: "log", Column: "email", DataType: "varchar", CharMax: 100}, // no id column
	{Table: "contacts", Column: "Apellido Paterno", DataType: "varchar", CharMax: 100, HasID: true},
	{Table: "contacts", Column: "Clave Secreta", DataType: "varchar", CharMax: 255, HasID: true},
}

func TestRenderIsAllComments(t *testing.T) {
	out := Render(engine.MySQL{}, "", sample)
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("a suggestion must be commented out: %q", line)
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil || len(doc) != 0 {
		t.Errorf("the file must be valid YAML with no active content: %v %v", doc, err)
	}
}

func TestSuggestions(t *testing.T) {
	out := Render(engine.MySQL{}, "", sample)
	for _, want := range []string{
		`#   email: "user-{id}@example.test"`,
		`#   phone: "+56900{(id % 1000000) + 1000000}"`,
		`#   second_phone: "{LEFT(MD5(CONCAT(id, '')), 8)}"`, // too short for the readable one
		`#   password: "{MD5(CONCAT(id, ''))}"`,
		`#   birthdate: "{MAKEDATE(YEAR(birthdate), 1)}"`,
		`#   valid_email: "{KEEP}"`, // a flag, not text
		`#   notes: "{NULL}"`,       // free text: decide
		`#   name: "{KEEP}"`,        // ambiguous name: decide
		`# log:`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `#   email: "{NULL}"   ## varchar(100): the table has no `+"`id`") { // the table without `id`
		t.Errorf("a table without id must not get id-based patterns:\n%s", out)
	}
	// a column name with a space is a quoted YAML key and is quoted again where the pattern refers to it
	for _, want := range []string{
		`#   "Apellido Paterno": "Apellido{id}"`,
		`#   "Clave Secreta": "{MD5(CONCAT(id, ''))}"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// no pattern may hash or rewrite its own column: the post-UPDATE check would never match a second evaluation
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "MD5(") && !strings.Contains(line, "MD5(CONCAT(id, ''))") {
			t.Errorf("a suggestion refers to its own column: %s", line)
		}
	}
	// md5() is text-only in PostgreSQL: the id must never be passed to it as an integer
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "MD5(id)") {
			t.Errorf("MD5(id) fails on PostgreSQL (md5(integer) does not exist): %s", line)
		}
	}
	pg := Render(engine.Postgres{}, "", sample)
	if !strings.Contains(pg, `{DATE_TRUNC('year', birthdate)::date}`) {
		t.Errorf("postgres dates:\n%s", pg)
	}
}

// Uncommenting the suggested lines must give a document columns.yaml can read.
func TestUncommentedIsValidColumnsYAML(t *testing.T) {
	var live []string
	for _, line := range strings.Split(Render(engine.MySQL{}, "", sample), "\n") {
		if strings.HasPrefix(line, "##") || line == "" {
			continue
		}
		live = append(live, strings.TrimPrefix(line, "# "))
	}
	var doc map[string]map[string]string
	if err := yaml.Unmarshal([]byte(strings.Join(live, "\n")), &doc); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(live, "\n"))
	}
	if doc["user"]["email"] != "user-{id}@example.test" || len(doc["log"]) != 1 {
		t.Errorf("%v", doc)
	}
}
