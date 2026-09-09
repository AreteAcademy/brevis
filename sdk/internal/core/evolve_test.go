package core

import (
	"strings"
	"testing"
)

var declared = Schema{
	{Name: "id", Type: TypeString, Required: true},
	{Name: "qty", Type: TypeInt64},
	{Name: "note", Type: TypeString, Default: "none"},
}

func TestNothingToDoWhenTheTableMatches(t *testing.T) {
	actual := map[string]ColumnType{"id": TypeString, "qty": TypeInt64, "note": TypeString}
	changes, err := declared.Plan(actual, EvolveAdditive, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("a matching table produced %v", changes)
	}
}

// A new column is ADDED, and only with the mode on. The default -- EvolveNone --
// is what every driver did before this existed, and a load that starts altering
// tables because a field defaulted to on is not a surprise anybody wants.
func TestANewColumnIsAddedOnlyWhenAsked(t *testing.T) {
	actual := map[string]ColumnType{"id": TypeString, "qty": TypeInt64}

	if _, err := declared.Plan(actual, EvolveNone, "bronze.orders"); err == nil {
		t.Fatal("EvolveNone accepted a table missing a declared column")
	} else if !strings.Contains(err.Error(), "EvolveAdditive") {
		t.Errorf("the refusal does not say how to allow it: %v", err)
	}

	changes, err := declared.Plan(actual, EvolveAdditive, "bronze.orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != "add" || changes[0].Column != "note" {
		t.Fatalf("changes = %v", changes)
	}
}

// TestWideningIsAllowedAndNarrowingIsNot.
//
// numeric -> float64 is the one somebody always asks for, and it is the one
// that silently rounds money. It is refused NAMING BOTH TYPES, because
// "schema mismatch" is a message people work around by dropping the table.
func TestWideningIsAllowedAndNarrowingIsNot(t *testing.T) {
	for _, c := range []struct {
		name     string
		declared ColumnType
		have     ColumnType
		ok       bool
	}{
		{"int stays int", TypeInt64, TypeInt64, true},
		{"int widens to float", TypeFloat64, TypeInt64, true},
		{"int widens to numeric", TypeNumeric, TypeInt64, true},
		{"float does NOT become numeric", TypeNumeric, TypeFloat64, false},
		{"numeric does NOT become float", TypeFloat64, TypeNumeric, false},
		{"int does NOT become string", TypeString, TypeInt64, false},
		{"string does NOT become int", TypeInt64, TypeString, false},
		{"timestamp does NOT become date", TypeDate, TypeTimestamp, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := Schema{{Name: "v", Type: c.declared}}
			changes, err := s.Plan(map[string]ColumnType{"v": c.have}, EvolveAdditive, "t")
			if c.ok {
				if err != nil {
					t.Fatalf("refused a widening: %v", err)
				}
				if c.declared != c.have && (len(changes) != 1 || changes[0].Kind != "widen") {
					t.Errorf("changes = %v", changes)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %s -> %s", c.have, c.declared)
			}
			for _, want := range []string{string(c.have), string(c.declared), "v"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

// A column the TABLE has and the declaration does not is left alone -- not
// dropped, and not an error. Dropping loses history, which is what a warehouse
// is for.
func TestAColumnTheDeclarationForgotIsLeftAlone(t *testing.T) {
	actual := map[string]ColumnType{
		"id": TypeString, "qty": TypeInt64, "note": TypeString,
		"legacy_flag": TypeBool,
	}
	changes, err := declared.Plan(actual, EvolveAdditive, "t")
	if err != nil {
		t.Fatalf("an extra column in the table was refused: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("an extra column produced %v", changes)
	}
}

// TestTheAddedColumnIsNullableWhateverTheDeclarationSays.
//
// NOT NULL would have to be true of every row already in the table, and no
// value this SDK could invent is. The declaration keeps describing what a NEW
// table gets.
func TestTheAddedColumnIsNullableWhateverTheDeclarationSays(t *testing.T) {
	s := Schema{{Name: "flag", Type: TypeBool, Required: true, Default: false}}
	stmts, err := s.AlterTable(Postgres, "bronze.orders",
		[]Change{{Column: "flag", Kind: "add", To: TypeBool}})
	if err != nil {
		t.Fatal(err)
	}
	// TWO statements, and the split is the point: `ADD COLUMN ... DEFAULT x`
	// backfills the rows already in the table, so a row loaded in March would
	// start claiming a value it never had.
	if len(stmts) != 2 {
		t.Fatalf("stmts = %v", stmts)
	}
	if strings.Contains(stmts[0], "NOT NULL") {
		t.Errorf("an added column was made NOT NULL:\n%s", stmts[0])
	}
	if strings.Contains(stmts[0], "DEFAULT") {
		t.Errorf("the ADD carries the default, which backfills:\n%s", stmts[0])
	}
	if stmts[0] != `ALTER TABLE "bronze"."orders" ADD COLUMN "flag" BOOLEAN` {
		t.Errorf("unexpected ADD:\n%s", stmts[0])
	}
	if stmts[1] != `ALTER TABLE "bronze"."orders" ALTER COLUMN "flag" SET DEFAULT false` {
		t.Errorf("unexpected SET DEFAULT:\n%s", stmts[1])
	}
}

// MySQL needs a different statement for the same thing, and the server is what
// said so: `ALTER COLUMN ... SET DEFAULT ('web')` is Error 1101 on a TEXT
// column, and `MODIFY COLUMN ... DEFAULT ('web')` is accepted. SET DEFAULT
// takes only a literal, and a TEXT default has to be an expression -- between
// the two constraints exactly one statement works.
func TestMySQLSetsADefaultByModifyingTheColumn(t *testing.T) {
	s := Schema{{Name: "channel", Type: TypeString, Default: "web"}}
	stmts, err := s.AlterTable(MySQL, "t", []Change{{Column: "channel", Kind: "add", To: TypeString}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 {
		t.Fatalf("stmts = %v", stmts)
	}
	if stmts[0] != "ALTER TABLE `t` ADD COLUMN `channel` LONGTEXT" {
		t.Errorf("unexpected ADD: %s", stmts[0])
	}
	want := "ALTER TABLE `t` MODIFY COLUMN `channel` LONGTEXT DEFAULT ('web')"
	if stmts[1] != want {
		t.Errorf("the default statement is\n  %s\nand has to be\n  %s", stmts[1], want)
	}
}

// The widening statement is the one whose SYNTAX differs, not just its type
// names -- MySQL has no ALTER COLUMN ... TYPE.
func TestWideningSpeaksEachDialect(t *testing.T) {
	s := Schema{{Name: "qty", Type: TypeNumeric}}
	ch := []Change{{Column: "qty", Kind: "widen", From: "int64", To: TypeNumeric}}

	pg, err := s.AlterTable(Postgres, "t", ch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pg[0], `ALTER COLUMN "qty" TYPE NUMERIC`) {
		t.Errorf("postgres: %s", pg[0])
	}

	my, err := s.AlterTable(MySQL, "t", ch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(my[0], "MODIFY COLUMN `qty` DECIMAL(38,9)") {
		t.Errorf("mysql: %s", my[0])
	}
}

// One statement per change, never a combined ALTER: the two dialects disagree
// about whether a multi-clause ALTER is atomic, and a half-applied one is the
// state this whole design exists to avoid.
func TestEachChangeIsItsOwnStatement(t *testing.T) {
	s := Schema{
		{Name: "a", Type: TypeString},
		{Name: "b", Type: TypeInt64},
	}
	stmts, err := s.AlterTable(Postgres, "t", []Change{
		{Column: "a", Kind: "add", To: TypeString},
		{Column: "b", Kind: "add", To: TypeInt64},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 {
		t.Fatalf("expected one statement per change, got %v", stmts)
	}
	for _, s := range stmts {
		if strings.Count(s, "ADD COLUMN") != 1 {
			t.Errorf("a statement carries more than one change: %s", s)
		}
	}
}
