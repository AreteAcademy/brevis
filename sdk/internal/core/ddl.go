package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dialect is a destination's SQL, for the two things a CREATE TABLE needs: what
// each declared type is called, and how an identifier is quoted.
//
// It is a value and not an interface because there is nothing to override: a
// dialect IS these two tables. An interface would invite a fifth destination to
// implement `TypeOf` with its own opinion, and the point of putting the types
// here is that there is one opinion.
type Dialect struct {
	// Name appears in errors: "postgres has no equivalent for json".
	Name string

	// Types maps every declared type to this dialect's spelling. A type that is
	// absent is refused by name rather than rendered as something close.
	Types map[ColumnType]string

	// Quote wraps an identifier. Nil means double quotes, which is the SQL
	// standard and what three of the four use.
	Quote func(string) string

	// NowExpr is what Now renders as. Empty means CURRENT_TIMESTAMP.
	NowExpr string

	// IfNotExists says whether the dialect accepts `CREATE TABLE IF NOT
	// EXISTS`. Redshift does; BigQuery's Go client creates through the API and
	// never sees this path.
	IfNotExists bool

	// ParenDefault names the types whose DEFAULT has to be parenthesised.
	//
	// It exists for exactly one dialect and one refusal, and it was found by a
	// real server rather than by reading a manual: MySQL rejects a plain
	// DEFAULT on a BLOB, TEXT or JSON column --
	//
	//     Error 1101: BLOB, TEXT, GEOMETRY or JSON column 'status'
	//     can't have a default value
	//
	// -- and accepts the same value written `DEFAULT ('pending')`, which 8.0.13
	// added as an "expression default". Since `string` is LONGTEXT here, a
	// default on any string column hit this.
	//
	// Sizing the column to a VARCHAR would also work and is the thing this SDK
	// refuses to do: a length is a guess about data it has not seen. Whoever
	// wants a sized column writes CreateSQL.
	//
	// On a server older than 8.0.13 this fails -- and so does every other way
	// of defaulting a TEXT column there, so nothing is lost.
	ParenDefault map[ColumnType]bool
}

// The four dialects. Written out rather than derived, because three of the
// entries are DECISIONS and a derivation would hide them:
//
//   - MySQL's string is LONGTEXT and not VARCHAR. A VARCHAR needs a length and
//     a length is a guess about data the SDK has not seen. A sized column is
//     CreateSQL's job.
//   - MySQL's bool is TINYINT(1), which is what BOOLEAN is an alias FOR.
//     Writing the alias would hide that a 2 fits in there.
//   - Redshift has no JSON. SUPER is the closest thing and it behaves
//     differently enough to be worth naming rather than pretending.
var (
	Postgres = Dialect{
		Name: "postgres",
		Types: map[ColumnType]string{
			TypeString: "TEXT", TypeInt64: "BIGINT", TypeFloat64: "DOUBLE PRECISION",
			TypeNumeric: "NUMERIC", TypeBool: "BOOLEAN", TypeTimestamp: "TIMESTAMPTZ",
			TypeDate: "DATE", TypeJSON: "JSONB", TypeBytes: "BYTEA",
		},
		IfNotExists: true,
	}

	MySQL = Dialect{
		Name: "mysql",
		Types: map[ColumnType]string{
			TypeString: "LONGTEXT", TypeInt64: "BIGINT", TypeFloat64: "DOUBLE",
			TypeNumeric: "DECIMAL(38,9)", TypeBool: "TINYINT(1)", TypeTimestamp: "DATETIME(6)",
			TypeDate: "DATE", TypeJSON: "JSON", TypeBytes: "LONGBLOB",
		},
		Quote:       func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" },
		NowExpr:     "CURRENT_TIMESTAMP(6)",
		IfNotExists: true,
		ParenDefault: map[ColumnType]bool{
			TypeString: true, TypeJSON: true, TypeBytes: true,
		},
	}

	Redshift = Dialect{
		Name: "redshift",
		Types: map[ColumnType]string{
			TypeString: "VARCHAR(65535)", TypeInt64: "BIGINT", TypeFloat64: "DOUBLE PRECISION",
			TypeNumeric: "DECIMAL(38,9)", TypeBool: "BOOLEAN", TypeTimestamp: "TIMESTAMPTZ",
			TypeDate: "DATE", TypeJSON: "SUPER", TypeBytes: "VARBYTE(1024000)",
		},
		IfNotExists: true,
	}
)

// quote wraps an identifier for this dialect.
//
// The doubling is not decoration. A column called `it"s` would otherwise close
// the quote and the rest of the name would be parsed as SQL -- and a Schema can
// be filled from a config file, which is somebody else's input.
func (d Dialect) quote(id string) string {
	if d.Quote != nil {
		return d.Quote(id)
	}
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}

// CreateTable renders the statement for this schema.
//
// The table name arrives already qualified ("bronze.orders") and is quoted
// segment by segment: quoting it whole would produce a table literally called
// `bronze.orders`, which is a different table and a very confusing one.
func (s Schema) CreateTable(d Dialect, table string) (string, error) {
	if err := s.Check(); err != nil {
		return "", err
	}
	if len(s) == 0 {
		return "", fmt.Errorf("cannot create %s: the Schema is empty", table)
	}

	cols := make([]string, 0, len(s))
	for _, c := range s {
		sqlType, known := d.Types[c.Type]
		if !known {
			return "", fmt.Errorf("column %q is %s, and %s has no equivalent. "+
				"Write the DDL in CreateSQL", c.Name, c.Type, d.Name)
		}
		line := d.quote(c.Name) + " " + sqlType
		if c.Default != nil {
			lit, err := d.literal(c)
			if err != nil {
				return "", err
			}
			if d.ParenDefault[c.Type] {
				lit = "(" + lit + ")"
			}
			line += " DEFAULT " + lit
		}
		// NOT NULL goes AFTER the default, which is the order every one of
		// these dialects parses. The other way round is a syntax error in
		// MySQL, and it is the kind that only shows up against a real server.
		if c.Required {
			line += " NOT NULL"
		}
		cols = append(cols, "  "+line)
	}

	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	if d.IfNotExists {
		b.WriteString("IF NOT EXISTS ")
	}
	b.WriteString(qualified(d, table))
	b.WriteString(" (\n")
	b.WriteString(strings.Join(cols, ",\n"))
	b.WriteString("\n)")
	return b.String(), nil
}

// qualified quotes "schema.table" one segment at a time.
func qualified(d Dialect, table string) string {
	parts := strings.Split(table, ".")
	for i, p := range parts {
		parts[i] = d.quote(p)
	}
	return strings.Join(parts, ".")
}

// literal renders a Go value as a SQL literal for this dialect.
//
// Only what a DEFAULT legitimately holds. A type this does not know is refused
// naming the type, rather than falling through to fmt.Sprintf -- which would
// turn a struct into `{1 2}` and put it in a DDL.
func (d Dialect) literal(c Column) (string, error) {
	switch v := c.Default.(type) {
	case Expression:
		if v != CurrentTimestamp {
			return "", fmt.Errorf("column %q has an unknown default expression", c.Name)
		}
		if d.NowExpr != "" {
			return d.NowExpr, nil
		}
		return "CURRENT_TIMESTAMP", nil
	case string:
		// Single quotes doubled. The same reason as the identifier: a Schema
		// can come from a file somebody else wrote.
		return "'" + strings.ReplaceAll(v, "'", "''") + "'", nil
	case bool:
		if d.Name == "mysql" {
			// TINYINT(1) takes 0 and 1. `DEFAULT TRUE` parses, and then
			// SHOW CREATE TABLE reads back `DEFAULT '1'` -- so writing what it
			// stores keeps the round trip honest.
			if v {
				return "1", nil
			}
			return "0", nil
		}
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case time.Time:
		return "'" + v.UTC().Format(time.RFC3339Nano) + "'", nil
	default:
		return "", fmt.Errorf("column %q has a default of type %T, which is not a SQL "+
			"literal. Use a string, a number, a bool, a time.Time or core.Now; "+
			"anything else belongs in CreateSQL", c.Name, c.Default)
	}
}
