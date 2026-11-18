package plan

import (
	"strings"
	"testing"

	"github.com/letkode/db-anon-dump/internal/config"
	"github.com/letkode/db-anon-dump/internal/engine"
)

func meta() map[string][]engine.Column {
	return map[string][]engine.Column{
		"user": {
			{Table: "user", Name: "id", DataType: "int"},
			{Table: "user", Name: "email", DataType: "varchar", CharMax: 255, Nullable: true, Unique: true},
			{Table: "user", Name: "notes", DataType: "longtext", Nullable: false},
			{Table: "user", Name: "meta", DataType: "json", Nullable: false},
			{Table: "user", Name: "firstname", DataType: "varchar", CharMax: 50},
			{Table: "user", Name: "ip_address", DataType: "varchar", CharMax: 45, Nullable: true},
		},
		"city": {{Table: "city", Name: "id", DataType: "int"}, {Table: "city", Name: "name", DataType: "varchar", CharMax: 255}},
	}
}

func rules() map[string]map[string]config.Rule {
	return map[string]map[string]config.Rule{
		"user": {
			"email":     {Pattern: "user-{id}@example.test"},
			"notes":     {Pattern: "{NULL}"},
			"meta":      {Pattern: "{NULL}"},
			"firstname": {Pattern: "Nombre{id}"},
		},
		"*": {"name": {Pattern: "{KEEP}"}},
	}
}

func TestBuild(t *testing.T) {
	p, errs := Build(engine.MySQL{}, []string{"user", "city"}, meta(), rules())
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(p.Tables) != 1 || p.Tables[0].Table != "user" {
		t.Fatalf("tables: %+v", p.Tables)
	}
	tp := p.Tables[0]
	upd := tp.UpdateSQL()
	for _, want := range []string{
		"`email` = CASE WHEN `email` IS NULL THEN NULL ELSE CONCAT('user-', id, '@example.test') END", // nullable keeps NULL
		"`notes` = ''",                      // NOT NULL text -> type default
		"`meta` = CAST('{}' AS JSON)",       // NOT NULL json -> '{}'
		"`firstname` = CONCAT('Nombre', id)", // NOT NULL: no wrapper
	} {
		if !strings.Contains(upd, want) {
			t.Errorf("UPDATE misses %q:\n%s", want, upd)
		}
	}
	if len(tp.Parks) != 1 || tp.Parks[0] != "email" {
		t.Errorf("parks: %v", tp.Parks)
	}
	if got := tp.ParkSQL("email"); !strings.Contains(got, "CONCAT('t-', `id`)") {
		t.Errorf("ParkSQL: %s", got)
	}
	if !strings.Contains(tp.CheckSQL(), "NOT (`firstname` <=> CONCAT('Nombre', id))") {
		t.Errorf("CheckSQL: %s", tp.CheckSQL())
	}
	// audit: ip_address looks personal and has no pattern; city.name is {KEEP}-ed by the wildcard
	if strings.Join(p.Audit, ",") != "user.ip_address (varchar)" {
		t.Errorf("audit: %v", p.Audit)
	}
}

func TestExactEntryBeatsWildcard(t *testing.T) {
	r := rules()
	r["*"]["firstname"] = config.Rule{Pattern: "wild{id}"}
	p, errs := Build(engine.MySQL{}, []string{"user"}, meta(), r)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if !strings.Contains(p.Tables[0].UpdateSQL(), "Nombre") || strings.Contains(p.Tables[0].UpdateSQL(), "wild") {
		t.Errorf("exact entry must win:\n%s", p.Tables[0].UpdateSQL())
	}
}

func TestErrorsAreAggregated(t *testing.T) {
	r := rules()
	r["user"]["no_such"] = config.Rule{Pattern: "{NULL}"}
	r["user"]["legacy"] = config.Rule{Pattern: "{NULL}", Optional: true} // optional: silently skipped
	r["user"]["firstname"] = config.Rule{Pattern: "bad{"}
	_, errs := Build(engine.MySQL{}, []string{"user"}, meta(), r)
	if len(errs) != 2 {
		t.Fatalf("want 2 errors, got %v", errs)
	}
}

func TestUniqueWithoutIDIsAnError(t *testing.T) {
	m := map[string][]engine.Column{"t": {{Table: "t", Name: "code", DataType: "varchar", CharMax: 10, Unique: true}}}
	_, errs := Build(engine.MySQL{}, []string{"t"}, m, map[string]map[string]config.Rule{"t": {"code": {Pattern: "x{1}"}}})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "no `id` column") {
		t.Fatalf("got %v", errs)
	}
}
