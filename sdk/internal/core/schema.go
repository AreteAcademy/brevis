package core

import (
	"fmt"
	"sort"
	"strings"
)

// ColumnType is the type of a declared column.
//
// The list is short on purpose. It is not any database's type system: it is the
// minimum set a JSON record produces, with one name per thing. Whoever needs
// NUMERIC(18,2), a sized VARCHAR or a type only one destination has writes the
// DDL in CreateSQL -- which goes on existing for exactly that.
type ColumnType string

const (
	// TypeString is text. In BigQuery, STRING.
	TypeString ColumnType = "string"

	// TypeInt64 is a 64-bit integer.
	TypeInt64 ColumnType = "int64"

	// TypeFloat64 is floating point. Do NOT use it for money: a float loses
	// cents on large values, and the damage turns up months later in a report
	// nobody double-checks. For money, TypeNumeric.
	TypeFloat64 ColumnType = "float64"

	// TypeNumeric is exact decimal.
	TypeNumeric ColumnType = "numeric"

	// TypeBool is a boolean.
	TypeBool ColumnType = "bool"

	// TypeTimestamp is an instant with a timezone.
	TypeTimestamp ColumnType = "timestamp"

	// TypeDate is a date with no time.
	TypeDate ColumnType = "date"

	// TypeJSON is a nested document.
	TypeJSON ColumnType = "json"

	// TypeBytes is binary.
	TypeBytes ColumnType = "bytes"
)

// Column is a declared column: the name, the type, and whether it takes null.
type Column struct {
	// Name is the column's name in the destination. Required.
	Name string

	// Type is the type. Required -- a blank type would be an inference wearing
	// a declaration's clothes.
	Type ColumnType

	// Required marks NOT NULL. The default is to take null, which is what a
	// landing table legitimately does: a column the source sometimes does not
	// send.
	Required bool

	// Default is the column's DEFAULT, and it is a Go value rather than a
	// string of SQL.
	//
	//	{Name: "status",   Type: TypeString,    Default: "pending"}
	//	{Name: "attempts", Type: TypeInt64,     Default: 0}
	//	{Name: "seen_at",  Type: TypeTimestamp, Default: CurrentTimestamp}
	//
	// A `Default string` holding raw SQL was the other candidate and it is a
	// DDL injection point in a field that reads like data: whoever fills a
	// Schema from a config file would be handing the destination a statement.
	// A Go literal is rendered per dialect and cannot be anything else.
	//
	// What a literal cannot express is `now()`, which is common enough that
	// leaving it out would send everybody to CreateSQL. CurrentTimestamp is
	// the one exception, and it is a TYPE rather than a magic string so that
	// `Default: "now()"` stays what it says it is -- the literal text.
	//
	// Anything past that -- a sequence, a computed column, NUMERIC(18,2) -- is
	// CreateSQL, which goes on existing for exactly that.
	//
	// nil means no DEFAULT clause. A column that should default to NULL simply
	// has none: writing DEFAULT NULL is what the database does anyway.
	Default any
}

// Expression is a default the database computes rather than stores. The only
// value is CurrentTimestamp, and the type exists so that a caller writing
// `Default: "now()"` gets the six-character string they wrote.
//
// It is not called Now: the SDK root already has a Now(), a FieldSelector for
// the transform chain, and a default that shadowed a transformer would be a
// collision nobody would enjoy debugging.
type Expression int

// CurrentTimestamp is the destination's CURRENT_TIMESTAMP, in whichever
// spelling it uses.
const CurrentTimestamp Expression = iota + 1

// Schema is the destination's declaration, in DDL order.
//
// It exists because the SDK does NOT infer types. A destination offering
// autodetect would make the types come from the data -- and one run's data is
// not the next run's: a field that arrived whole today and fractional tomorrow
// changes the column's type with nobody writing anything.
//
//	Schema: sdk.Schema{
//	    {Name: "ingestion_id",        Type: sdk.TypeString,    Required: true},
//	    {Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
//	    {Name: "temperature",         Type: sdk.TypeFloat64},
//	}
type Schema []Column

// Names returns the names, in declared order.
func (s Schema) Names() []string {
	out := make([]string, len(s))
	for i, c := range s {
		out[i] = c.Name
	}
	return out
}

// Has says whether the column is declared.
func (s Schema) Has(name string) bool {
	for _, c := range s {
		if c.Name == name {
			return true
		}
	}
	return false
}

// Check refuses a declaration that cannot be right.
func (s Schema) Check() error {
	if len(s) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(s))
	var duplicated, unnamed, untyped, badType []string

	for i, c := range s {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			unnamed = append(unnamed, fmt.Sprintf("position %d", i))
			continue
		}
		if seen[name] {
			duplicated = append(duplicated, name)
		}
		seen[name] = true

		switch c.Type {
		case "":
			untyped = append(untyped, name)
		case TypeString, TypeInt64, TypeFloat64, TypeNumeric,
			TypeBool, TypeTimestamp, TypeDate, TypeJSON, TypeBytes:
		default:
			badType = append(badType, fmt.Sprintf("%s (%q)", name, c.Type))
		}
	}

	for _, p := range []struct {
		list []string
		msg  string
	}{
		{unnamed, "Schema has a column with no name at %s"},
		{duplicated, "Schema declares %s more than once, and the second one would be ignored"},
		{untyped, "Schema leaves %s without a Type -- a blank type would be an inference " +
			"wearing a declaration's clothes. Use sdk.TypeString, TypeInt64, TypeFloat64, " +
			"TypeNumeric, TypeBool, TypeTimestamp, TypeDate, TypeJSON or TypeBytes"},
		{badType, "Schema uses a type that does not exist: %s"},
	} {
		if len(p.list) > 0 {
			sort.Strings(p.list)
			return fmt.Errorf(p.msg, strings.Join(p.list, ", "))
		}
	}
	return nil
}
