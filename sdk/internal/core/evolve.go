package core

import (
	"fmt"
	"sort"
	"strings"
)

// Evolution says what a load may do to a table that already exists and no
// longer matches the declaration.
type Evolution int

const (
	// EvolveNone refuses any difference. It is the behaviour every driver had
	// before this existed, and it stays the zero value: a load that starts
	// altering tables because a field defaulted to on is not a surprise anybody
	// wants.
	EvolveNone Evolution = iota

	// EvolveAdditive adds columns the declaration has and the table does not,
	// and widens a type where widening loses nothing. Everything else is
	// refused, naming both sides.
	//
	// This is what Fivetran, Airbyte, Delta Lake and Iceberg all converge on,
	// and the narrowness is the point: adding a nullable column cannot break a
	// reader, and every other change can.
	EvolveAdditive

	// EvolveAdditiveFromPayload is EvolveAdditive plus the columns the BATCH
	// carries and the declaration does not.
	//
	// The type comes from TypeFromShape and never from the value, so this is
	// not the SDK inferring a schema: `STRING` unless the field is an object
	// or an array is a RULE, the same one the gateway lands `shape: columns`
	// with. What it decides is how the ROW's declaration is completed, never
	// what a column means.
	//
	// It still only ADDS. Plan emits `add` and `widen` and nothing else, so a
	// field that disappears from the source stops being written and its column
	// stays -- which is not a gap, it is what a landing table is for.
	EvolveAdditiveFromPayload
)

// MayAdd reports whether this mode adds a column the table lacks.
//
// A PREDICATE and not a comparison, and the difference is the only reason it
// exists. `mode == EvolveAdditive` answers "no" for every value added to this
// enum after it -- silently, so the new mode is not refused, it simply does
// nothing. Four of the places that asked it that way would have made a third
// mode LESS capable than EvolveAdditive, and one of them returns early from
// evolveTable, which would have shipped it inert on BigQuery.
//
// That is not hypothetical. metadata.go records this SDK paying for it once:
// "the feature would have been inert the day it shipped. It was found by
// loading against a real Postgres, not by reading the code."
//
// Ordered rather than enumerated: a mode that adds is at least EvolveAdditive,
// and anything past it adds too. A new value that does NOT add would have to
// sit before it, which is a decision somebody makes on purpose -- and the
// zero value is still EvolveNone, so the default is still "refuse".
func (e Evolution) MayAdd() bool { return e >= EvolveAdditive }

// FromPayload reports whether the BATCH may contribute columns the
// declaration does not have.
//
// Named rather than compared, for the reason MayAdd is: a mode added later
// that also reads the payload must not have to remember to update a list of
// equalities spread across four packages.
func (e Evolution) FromPayload() bool { return e == EvolveAdditiveFromPayload }

// There is deliberately no EvolveAll.
//
// A mode that drops columns is a mode somebody switches on during an incident
// and discovers a quarter later, when the history it deleted is the thing they
// need. A column that disappears from the source stops being written and stays
// in the table; that is not a gap in this feature, it is the feature.

// Change is one alteration the load would make.
type Change struct {
	Column string
	// Kind is "add" or "widen".
	Kind string
	// From is the current SQL type, empty for an add. To is the declared type.
	From string
	To   ColumnType
}

func (c Change) String() string {
	if c.Kind == "add" {
		return fmt.Sprintf("add %s %s", c.Column, c.To)
	}
	return fmt.Sprintf("widen %s from %s to %s", c.Column, c.From, c.To)
}

// widenings are the type changes that lose nothing, keyed by the DECLARED type
// and holding the current ones it may replace.
//
// The list is short and one-directional on purpose. int64 -> float64 is here
// because every int64 has an exact float64 up to 2^53 and beyond that the
// column was already the wrong type; float64 -> numeric is NOT here, because a
// float that has already lost cents does not get them back.
//
// Nothing narrows. numeric -> float64 is the one somebody always asks for, and
// it is the one that silently rounds money.
var widenings = map[ColumnType]map[ColumnType]bool{
	TypeInt64:   {TypeInt64: true},
	TypeFloat64: {TypeInt64: true, TypeFloat64: true},
	TypeNumeric: {TypeInt64: true, TypeNumeric: true},
	TypeString:  {TypeString: true},
}

// Plan compares the declaration against the table as it is now.
//
// `actual` maps a column name to the declared type it currently holds -- the
// driver reads its own catalogue and translates, because only the driver knows
// that `character varying` and `text` are both TypeString here.
//
// It returns the changes to make, or an error naming what cannot be made. The
// diff runs BEFORE the load, never during: a load that half-evolves and then
// fails leaves a table that is neither what it was nor what was declared, and
// the next run's diff starts from a shape nobody chose.
func (s Schema) Plan(actual map[string]ColumnType, mode Evolution, table string) ([]Change, error) {
	if len(s) == 0 || len(actual) == 0 {
		return nil, nil
	}

	var changes []Change
	var refused []string

	for _, c := range s {
		have, present := actual[c.Name]
		if !present {
			if !mode.MayAdd() {
				refused = append(refused, fmt.Sprintf(
					"%s is declared and the table does not have it", c.Name))
				continue
			}
			changes = append(changes, Change{Column: c.Name, Kind: "add", To: c.Type})
			continue
		}
		if have == c.Type {
			continue
		}
		if mode.MayAdd() && widenings[c.Type][have] {
			changes = append(changes, Change{
				Column: c.Name, Kind: "widen", From: string(have), To: c.Type,
			})
			continue
		}
		// A column a BATCH created is refused in the same words plus one:
		// without it the message reads as though somebody declared this and
		// got it wrong, and nobody did. What the reader should do differs --
		// there is no declaration of theirs to fix.
		why := fmt.Sprintf("%s is %s in the table and %s in the declaration",
			c.Name, have, c.Type)
		if c.Note != "" {
			why += fmt.Sprintf(" (%s)", c.Note)
		}
		refused = append(refused, why)
	}

	// A column the table has and the declaration does not is NOT a change. It
	// stops being written and it stays: dropping it loses history, and history
	// is the thing a warehouse is for. Saying nothing about it is deliberate --
	// warning on every load about a column somebody removed on purpose is how a
	// log stops being read.

	if len(refused) > 0 {
		sort.Strings(refused)
		verb := "does not match"
		how := "Set Evolve to sdk.EvolveAdditive to add the missing columns"
		if mode.MayAdd() {
			how = "A narrowing or a change of kind is refused: rename the column, " +
				"or migrate it yourself"
		}
		return nil, fmt.Errorf("%s %s the declaration: %s. %s",
			table, verb, strings.Join(refused, "; "), how)
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Column < changes[j].Column })
	return changes, nil
}

// AlterTable renders the statements for a plan, one per change.
//
// One statement each rather than a single ALTER with several clauses: Postgres
// and MySQL both accept the combined form and they disagree about whether it is
// atomic, and a partially applied multi-clause ALTER is the state this whole
// design is trying not to produce.
func (s Schema) AlterTable(d Dialect, table string, changes []Change) ([]string, error) {
	byName := make(map[string]Column, len(s))
	for _, c := range s {
		byName[c.Name] = c
	}

	out := make([]string, 0, len(changes))
	for _, ch := range changes {
		col, ok := byName[ch.Column]
		if !ok {
			return nil, fmt.Errorf("the plan names %q, which the Schema does not declare", ch.Column)
		}
		sqlType, known := d.Types[col.Type]
		if !known {
			return nil, fmt.Errorf("column %q is %s, and %s has no equivalent",
				col.Name, col.Type, d.Name)
		}

		switch ch.Kind {
		case "add":
			// A column added to a table with rows in it is NULLABLE, whatever
			// the declaration says. NOT NULL would have to be true of every row
			// that already exists, and no value this SDK could invent is. The
			// declaration keeps describing what a NEW table gets.
			add := "ALTER TABLE " + qualified(d, table) +
				" ADD COLUMN " + d.quote(col.Name) + " " + sqlType
			// The note travels WITH the ADD, in the same statement group, so
			// a column cannot come into being without the sentence saying
			// where it came from. MySQL carries it inside the definition and
			// has no COMMENT ON COLUMN at all; the others take their own
			// statement.
			if col.Note != "" && !d.NoComment {
				if d.InlineComment {
					add += " COMMENT " + sqlQuote(col.Note)
					out = append(out, add)
				} else {
					out = append(out, add,
						"COMMENT ON COLUMN "+qualified(d, table)+"."+d.quote(col.Name)+
							" IS "+sqlQuote(col.Note))
				}
			} else {
				out = append(out, add)
			}

			// The DEFAULT is set in a SECOND statement, and that is not
			// tidiness. `ADD COLUMN ... DEFAULT x` BACKFILLS the rows already
			// in the table -- Postgres has done that since 11, MySQL does it
			// too -- so a row loaded in March would start claiming a value it
			// never had. Inventing history is the thing this SDK refuses
			// everywhere else, and it took a real server to notice: the unit
			// test asserted the statement, and the statement was fine.
			//
			// Split, the old rows stay NULL and everything written after this
			// gets the default, which is what declaring one means.
			if col.Default != nil {
				lit, err := d.literal(col)
				if err != nil {
					return nil, err
				}
				if d.ParenDefault[col.Type] {
					lit = "(" + lit + ")"
				}
				out = append(out, d.setDefault(table, col.Name, sqlType, lit))
			}
		case "widen":
			out = append(out, d.alterType(table, col.Name, sqlType))
		default:
			return nil, fmt.Errorf("unknown change %q for %s", ch.Kind, ch.Column)
		}
	}
	return out, nil
}

// setDefault is how this dialect gives an EXISTING column a default.
//
// MySQL needs a different statement depending on the column's type, and the
// server is what said so:
//
//	ALTER COLUMN c SET DEFAULT ('web')          Error 1101 on a TEXT column
//	MODIFY COLUMN c LONGTEXT DEFAULT ('web')    accepted
//
// SET DEFAULT takes a literal only, and a TEXT column's default has to be an
// expression -- between the two constraints exactly one statement works. MODIFY
// restates the type, which is why this needs it.
func (d Dialect) setDefault(table, column, sqlType, lit string) string {
	if d.Name == "mysql" {
		return "ALTER TABLE " + qualified(d, table) + " MODIFY COLUMN " +
			d.quote(column) + " " + sqlType + " DEFAULT " + lit
	}
	return "ALTER TABLE " + qualified(d, table) + " ALTER COLUMN " +
		d.quote(column) + " SET DEFAULT " + lit
}

// alterType is the one statement whose SYNTAX differs between these dialects,
// rather than only its type names.
func (d Dialect) alterType(table, column, sqlType string) string {
	if d.Name == "mysql" {
		return "ALTER TABLE " + qualified(d, table) + " MODIFY COLUMN " +
			d.quote(column) + " " + sqlType
	}
	return "ALTER TABLE " + qualified(d, table) + " ALTER COLUMN " +
		d.quote(column) + " TYPE " + sqlType
}

// sqlQuote renders a string literal.
//
// Doubling the quote is the whole of it, and it is here rather than left to
// a driver's parameters because DDL takes no parameters: an ALTER is text or
// it is nothing. A note carrying an apostrophe -- "it's from a batch" -- would
// otherwise end the literal, and everything after it would be SQL.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
