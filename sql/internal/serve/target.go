// Package serve answers read-only questions about a warehouse, over HTTP, so
// the console never holds a warehouse credential.
//
// Everything here runs on the far side of that line. The console forwards a
// request; this decides what may run, bounds it, runs it and says what it
// did. A limit the console could change is not a limit, which is why none of
// them live there.
package serve

import (
	"fmt"
	"regexp"
	"strings"
)

// identifier is a name BigQuery can hold unquoted.
//
// THE SAME RULE THE DIALECT'S Build APPLIES, and deliberately a copy rather
// than a shared constant: that one guards what this project WRITES, this one
// guards what a browser may ASK ABOUT. They agree today and the day they stop
// agreeing, each should be able to change without the other -- a build that
// grew quoted names must not silently widen what a URL may contain.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// project is looser: a Google project id takes dashes and digits, and it
// never reaches SQL as an identifier -- it chooses the CONNECTION.
var project = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// Table is a target this service can read.
type Table struct {
	// Connection is which warehouse: the BigQuery project.
	Connection string
	// Dialect is whose SQL this is, which the classifier needs: `#` opens a
	// comment in one of them and is an operator in the other, and reading
	// the wrong one wrong is how `# SELECT\nDROP TABLE t` gets through.
	Dialect string
	// Relation is `dataset.table`, ready to be written into SQL -- which is
	// safe only because both halves matched `identifier`.
	Relation string
}

// ParseTarget turns a catalog target into something readable, or refuses it.
//
// THE MOST DANGEROUS FUNCTION IN THIS PACKAGE. What it returns is written
// into a statement, so nothing here escapes an input and then uses it:
// every segment must ALREADY be a name BigQuery could hold, and anything
// else is refused whole. A semicolon, a comment marker, a backtick or a
// space each turn one statement into two.
//
// BIGQUERY ONLY, FOR NOW, and that is a shape rather than an omission: a
// `bigquery://` target carries its project, and the project IS the
// connection. `postgres://database/schema/table` carries a database name and
// no host, port or user -- a target is an identity and never an address --
// so it cannot be reached without a connection somebody declared, which is a
// later slice.
//
// `s3`, `gs`, `file` and `pubsub` are not tables. They are in the catalog
// because Brevis lands them, and they have no rows to preview.
func ParseTarget(target string) (Table, error) {
	refuse := func(why string) (Table, error) {
		// THE INPUT IS NOT PASTED INTO THE MESSAGE. This reaches a screen,
		// and a refusal that echoes what it refused is the injection it
		// refused, one layer up.
		return Table{}, fmt.Errorf("this target cannot be previewed: %s. "+
			"A readable target is `bigquery://project/dataset/table`", why)
	}

	rest, ok := strings.CutPrefix(target, "bigquery://")
	if !ok {
		return refuse("only BigQuery destinations can be read today")
	}
	segs := strings.Split(rest, "/")
	if len(segs) != 3 {
		return refuse(fmt.Sprintf("it has %d path segments and a table has 3", len(segs)))
	}
	if !project.MatchString(segs[0]) {
		return refuse("the project is not a Google project id")
	}
	for _, seg := range segs[1:] {
		if !identifier.MatchString(seg) || len(seg) > 1024 {
			return refuse("the dataset or the table is not a name BigQuery can hold unquoted")
		}
	}
	return Table{Connection: segs[0], Dialect: "bigquery", Relation: segs[1] + "." + segs[2]}, nil
}
