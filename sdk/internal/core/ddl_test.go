package core

import (
	"strings"
	"testing"
	"time"
)

var sample = Schema{
	{Name: "id", Type: TypeString, Required: true},
	{Name: "status", Type: TypeString, Default: "pending"},
	{Name: "attempts", Type: TypeInt64, Default: 0},
	{Name: "active", Type: TypeBool, Default: true},
	{Name: "amount", Type: TypeNumeric},
	{Name: "seen_at", Type: TypeTimestamp, Default: CurrentTimestamp},
	{Name: "payload", Type: TypeJSON},
}

// A default on a TEXT, JSON or BLOB column is parenthesised for MySQL, and for
// nobody else. Error 1101 is what the plain form gets, and only a real server
// says so.
func TestMySQLParenthesisesTheDefaultsItHasTo(t *testing.T) {
	for _, c := range []struct {
		ct   ColumnType
		want string
	}{
		{TypeString, "DEFAULT ('x')"},
		{TypeInt64, "DEFAULT 1"},
		{TypeBool, "DEFAULT 1"},
	} {
		var def any = "x"
		switch c.ct {
		case TypeInt64:
			def = 1
		case TypeBool:
			def = true
		}
		got, err := Schema{{Name: "c", Type: c.ct, Default: def}}.CreateTable(MySQL, "t")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: missing %q in %s", c.ct, c.want, got)
		}
	}
	// And Postgres does not parenthesise, because it has no reason to.
	got, _ := Schema{{Name: "c", Type: TypeString, Default: "x"}}.CreateTable(Postgres, "t")
	if strings.Contains(got, "DEFAULT ('x')") {
		t.Errorf("postgres parenthesised a default it does not need to: %s", got)
	}
}

// TestEachDialectSpellsEveryType.
//
// A type with no entry renders as nothing at all, and the failure lands on the
// database as a syntax error about a column nobody can find. This asserts the
// tables are COMPLETE rather than that one lookup works.
func TestEachDialectSpellsEveryType(t *testing.T) {
	all := []ColumnType{
		TypeString, TypeInt64, TypeFloat64, TypeNumeric,
		TypeBool, TypeTimestamp, TypeDate, TypeJSON, TypeBytes,
	}
	for _, d := range []Dialect{Postgres, MySQL, Redshift} {
		for _, ct := range all {
			if d.Types[ct] == "" {
				t.Errorf("%s has no spelling for %s", d.Name, ct)
			}
		}
		if len(d.Types) != len(all) {
			t.Errorf("%s maps %d types and there are %d", d.Name, len(d.Types), len(all))
		}
	}
}

func TestTheStatementSaysWhatWasDeclared(t *testing.T) {
	for _, c := range []struct {
		dialect Dialect
		want    []string
	}{
		{Postgres, []string{
			`CREATE TABLE IF NOT EXISTS "bronze"."orders"`,
			`"id" TEXT NOT NULL`,
			`"status" TEXT DEFAULT 'pending'`,
			`"attempts" BIGINT DEFAULT 0`,
			`"active" BOOLEAN DEFAULT true`,
			`"seen_at" TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP`,
			`"payload" JSONB`,
		}},
		{MySQL, []string{
			"CREATE TABLE IF NOT EXISTS `bronze`.`orders`",
			"`id` LONGTEXT NOT NULL",
			// Parenthesised: MySQL refuses a plain DEFAULT on a TEXT column
			// and accepts the same value as an expression default.
			"`status` LONGTEXT DEFAULT ('pending')",
			// TINYINT(1) takes 0 and 1, and writing what it STORES keeps
			// SHOW CREATE TABLE honest.
			"`active` TINYINT(1) DEFAULT 1",
			"`seen_at` DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6)",
		}},
		{Redshift, []string{
			`"payload" SUPER`,
			`"id" VARCHAR(65535) NOT NULL`,
		}},
	} {
		t.Run(c.dialect.Name, func(t *testing.T) {
			got, err := sample.CreateTable(c.dialect, "bronze.orders")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

// TestTheQualifiedNameIsQuotedSegmentBySegment.
//
// Quoting "bronze.orders" whole produces a table literally called
// `bronze.orders` -- which is a different table, and one that works until
// somebody looks for it.
func TestTheQualifiedNameIsQuotedSegmentBySegment(t *testing.T) {
	got, _ := sample.CreateTable(Postgres, "bronze.orders")
	if strings.Contains(got, `"bronze.orders"`) {
		t.Errorf("the schema and the table were quoted together:\n%s", got)
	}
}

// TestNotNullComesAfterTheDefault.
//
// The other order is a syntax error in MySQL, and it is the kind that only
// shows up against a real server -- which is why this asserts the ORDER and not
// just that both appear.
func TestNotNullComesAfterTheDefault(t *testing.T) {
	s := Schema{{Name: "status", Type: TypeString, Required: true, Default: "new"}}
	got, err := s.CreateTable(MySQL, "t")
	if err != nil {
		t.Fatal(err)
	}
	def, notNull := strings.Index(got, "DEFAULT"), strings.Index(got, "NOT NULL")
	if def < 0 || notNull < 0 || def > notNull {
		t.Errorf("DEFAULT has to come before NOT NULL:\n%s", got)
	}
}

// TestAnIdentifierCannotEscapeItsQuotes.
//
// A Schema can be filled from a config file, which is somebody else's input. A
// column named `it"s` would close the quote and the rest would be parsed as
// SQL.
func TestAnIdentifierCannotEscapeItsQuotes(t *testing.T) {
	s := Schema{{Name: `it"s`, Type: TypeString, Default: "o'clock"}}
	got, err := s.CreateTable(Postgres, "t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"it""s"`) {
		t.Errorf("the identifier's quote was not doubled:\n%s", got)
	}
	if !strings.Contains(got, `'o''clock'`) {
		t.Errorf("the literal's quote was not doubled:\n%s", got)
	}

	s = Schema{{Name: "a`b", Type: TypeString}}
	got, _ = s.CreateTable(MySQL, "t")
	if !strings.Contains(got, "`a``b`") {
		t.Errorf("mysql's backtick was not doubled:\n%s", got)
	}
}

// TestADefaultThatIsNotALiteralIsRefused.
//
// Falling through to fmt.Sprintf would turn a struct into `{1 2}` and put it in
// a DDL, which is both wrong and the sort of wrong that reaches production.
func TestADefaultThatIsNotALiteralIsRefused(t *testing.T) {
	s := Schema{{Name: "x", Type: TypeString, Default: struct{ A int }{1}}}
	_, err := s.CreateTable(Postgres, "t")
	if err == nil {
		t.Fatal("a struct was accepted as a DEFAULT")
	}
	if !strings.Contains(err.Error(), "CreateSQL") {
		t.Errorf("the error does not say where such a default belongs: %v", err)
	}

	// And the ones that ARE literals.
	for _, v := range []any{"s", 1, int64(2), 1.5, true, time.Now(), CurrentTimestamp} {
		s := Schema{{Name: "x", Type: TypeTimestamp, Default: v}}
		if _, err := s.CreateTable(Postgres, "t"); err != nil {
			t.Errorf("%T was refused: %v", v, err)
		}
	}
}

// A type the dialect does not have is refused BY NAME, and the error says where
// to go instead. Rendering something close is how a NUMERIC becomes a FLOAT and
// the cents disappear months later.
func TestAMissingTypeIsRefusedByName(t *testing.T) {
	d := Dialect{Name: "toy", Types: map[ColumnType]string{TypeString: "TEXT"}}
	_, err := Schema{{Name: "amount", Type: TypeNumeric}}.CreateTable(d, "t")
	if err == nil {
		t.Fatal("a type the dialect lacks was rendered anyway")
	}
	for _, want := range []string{"amount", "numeric", "toy", "CreateSQL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// An empty Schema is refused, because CREATE TABLE t () is a statement that
// succeeds on some engines and produces a table no load can use.
func TestAnEmptySchemaIsRefused(t *testing.T) {
	if _, err := (Schema{}).CreateTable(Postgres, "t"); err == nil {
		t.Fatal("an empty Schema produced a CREATE")
	}
}
