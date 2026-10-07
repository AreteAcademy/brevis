package core

import (
	"fmt"
	"strings"
)

// Locator names a destination so that two runs writing the same table agree on
// the name, and two databases holding a table of the same name do not.
//
// It is an IDENTITY, not an address: no host, no port, no user, no password.
// A host is a deployment detail that changes with a failover, and a DSN is the
// one string in a pipeline most likely to carry a credential -- a catalog
// keyed by either would split one table into two the day the database moved,
// or publish a password to everyone who can open the console.
//
// Computing it never touches the network. The gateway calls it in a deploy
// pipeline with no route to any warehouse, and a step calls it after the
// load, when a slow answer would hold the pod for nothing.
//
// Optional on purpose, like DestinationChecker: a writer somebody wrote for
// their own destination keeps working without it, and simply lands nothing in
// the catalog.
type Locator interface {
	// Locate returns the destination's target, or "" when this writer cannot
	// name it -- an empty answer lands nothing, which is better than a guess.
	Locate() string
}

// The schemes a target may use, and how many path segments a table scheme
// takes. The list is closed for the same reason the engine's list of phases
// is: a scheme nobody reads would become a row nobody can group.
var tableSegments = map[string]int{
	"bigquery": 3, // project / dataset / table
	"postgres": 3, // database / schema / table
	"redshift": 3, // database / schema / table
	"mysql":    2, // database / table
	"pubsub":   2, // project / topic
}

var objectSchemes = map[string]bool{"s3": true, "gs": true, "file": true}

// targetCeiling bounds a target's length. It is a key in an index and a cell
// on a screen, and nothing that names a table needs more.
const targetCeiling = 512

// CheckTarget reports whether s has the shape of a target.
//
// It judges the SHAPE, and only that: it cannot know whether the table exists,
// and it does not try. What it refuses is everything that makes a target an
// address instead of a name -- userinfo, a port, a query string -- because
// those are what a DSN pasted in the wrong place looks like.
//
// The cases live in sdk/testdata/targets.txt, which the engine and the Python
// library copy. The three checkers agree because they read the same file.
func CheckTarget(s string) error {
	return checkTarget(s, false)
}

func checkTarget(s string, allowPattern bool) error {
	refuse := func(why string) error { return fmt.Errorf("target %q: %s", s, why) }

	if s == "" {
		return refuse("empty")
	}
	if len(s) > targetCeiling {
		return refuse(fmt.Sprintf("longer than %d bytes", targetCeiling))
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return refuse("no scheme: a target starts with one, as in bigquery://project/dataset/table")
	}
	_, table := tableSegments[scheme]
	if !table && !objectSchemes[scheme] {
		lower := strings.ToLower(scheme)
		if _, known := tableSegments[lower]; known || objectSchemes[lower] {
			return refuse("the scheme must be lower-case")
		}
		return refuse(fmt.Sprintf("unknown scheme %q", scheme))
	}
	for i := 0; i < len(rest); i++ {
		switch b := rest[i]; {
		case b <= ' ' || b == 0x7f:
			return refuse("contains whitespace or a control character")
		case b == '@':
			return refuse("contains '@': a target never carries a user, so this looks like a DSN")
		case b == '?':
			return refuse("contains a query string")
		case b == '#':
			return refuse("contains a fragment")
		}
	}

	if table {
		return checkTableTarget(scheme, rest, allowPattern, refuse)
	}
	return checkObjectTarget(scheme, rest, refuse)
}

func checkTableTarget(scheme, rest string, allowPattern bool, refuse func(string) error) error {
	segs := strings.Split(rest, "/")
	if want := tableSegments[scheme]; len(segs) != want {
		return refuse(fmt.Sprintf("%s takes %d path segments, found %d", scheme, want, len(segs)))
	}
	last := len(segs) - 1
	for i, seg := range segs {
		if seg == "" {
			return refuse("an empty path segment")
		}
		// A colon after the scheme is a port in every scheme but one: a
		// domain-scoped BigQuery project is spelled `example.com:project`.
		if strings.Contains(seg, ":") && !(scheme == "bigquery" && i == 0) {
			return refuse("contains ':' -- a port is an address, not a name")
		}
		if strings.Contains(seg, "*") {
			// A pattern names every table a gateway's auto_table may create
			// under one dataset. It is the whole last segment or nothing.
			if seg != "*" || i != last || !allowPattern || scheme == "pubsub" {
				return refuse("a '*' is only valid as the whole last segment of a published gateway pattern")
			}
		}
	}
	return nil
}

func checkObjectTarget(scheme, rest string, refuse func(string) error) error {
	if strings.ContainsAny(rest, ":*") {
		return refuse("an object target takes no ':' and no pattern")
	}
	if scheme == "file" {
		if !strings.HasPrefix(rest, "/") {
			return refuse("a file target is an absolute path: file:///dir/")
		}
		rest = rest[1:]
		if rest == "" {
			return nil
		}
	}
	segs := strings.Split(rest, "/")
	if scheme != "file" && segs[0] == "" {
		return refuse("no bucket")
	}
	for i, seg := range segs {
		// The trailing slash of a prefix leaves one empty segment at the end;
		// anywhere else an empty segment is `a//b`, which names nothing.
		if seg == "" && i != len(segs)-1 {
			return refuse("an empty path segment")
		}
	}
	return nil
}

// BigQueryTarget names a BigQuery table: bigquery://project/dataset/table.
//
// A partition decorator is stripped, because `clicks$20261007` and
// `clicks$20261008` are one table loaded on two days, and the catalog must
// show one row with two loads rather than two tables.
func BigQueryTarget(project, dataset, table string) string {
	unquote := func(s string) string { return strings.Trim(s, "`") }
	table, _, _ = strings.Cut(unquote(table), "$")
	return tableTarget("bigquery", []bool{true, false, false},
		unquote(project), unquote(dataset), table)
}

// PostgresTarget names a table in Postgres or Redshift:
// postgres://database/schema/table, or redshift://… with the same shape.
//
// An unquoted identifier is folded to lower case, which is what the database
// itself does with it: `Orders` and `orders` are one table, and writing them as
// two would split it in the catalog. A quoted one keeps its case.
func PostgresTarget(scheme, database, schema, table string) string {
	if scheme != "postgres" && scheme != "redshift" {
		return ""
	}
	return tableTarget(scheme, nil, database, foldIdent(schema), foldIdent(table))
}

// MySQLTarget names a MySQL table: mysql://database/table. Case is kept:
// whether MySQL folds it depends on the server's lower_case_table_names, which
// this cannot see without a connection.
func MySQLTarget(database, table string) string {
	unquote := func(s string) string { return strings.Trim(s, "`") }
	return tableTarget("mysql", nil, unquote(database), unquote(table))
}

// PubSubTarget names a Pub/Sub topic: pubsub://project/topic.
func PubSubTarget(project, topic string) string {
	return tableTarget("pubsub", nil, project, topic)
}

// ObjectTarget names the PREFIX a writer puts objects under:
// s3://bucket/prefix/, gs://bucket/prefix/, file:///dir/.
//
// The prefix and not the object, because a writer stamps each object's name
// with the time it was written: naming the object would make every run a new
// destination, and the catalog would grow one row per load forever.
func ObjectTarget(scheme, bucket, prefix string) string {
	var segs []string
	for _, seg := range strings.Split(prefix, "/") {
		if seg != "" {
			segs = append(segs, escapeSegment(seg, false))
		}
	}
	path := strings.Join(segs, "/")
	if path != "" {
		path += "/"
	}
	switch scheme {
	case "s3", "gs":
		if bucket == "" {
			return ""
		}
		return scheme + "://" + escapeSegment(bucket, false) + "/" + path
	case "file":
		if !strings.HasPrefix(prefix, "/") {
			return ""
		}
		return "file:///" + path
	}
	return ""
}

// tableTarget joins escaped segments under a scheme, or returns "" when any
// segment is missing: a target with a hole in it would group unrelated tables.
// colon[i] lets segment i keep a ':' (a domain-scoped BigQuery project).
func tableTarget(scheme string, colon []bool, parts ...string) string {
	segs := make([]string, len(parts))
	for i, p := range parts {
		if p == "" {
			return ""
		}
		segs[i] = escapeSegment(p, i < len(colon) && colon[i])
	}
	return scheme + "://" + strings.Join(segs, "/")
}

// foldIdent applies Postgres's rule for one identifier: quoted keeps its case
// (with "" unescaped), unquoted is folded.
func foldIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return strings.ToLower(s)
}

// escapeSegment percent-encodes everything in a name that the shape check
// would read as structure -- '/', '@', '?', '#', ':', '*', whitespace -- so a
// quoted identifier with a slash in it stays one segment instead of becoming
// two. The URI's unreserved characters and sub-delimiters pass through, which
// keeps `dia=1` and `my-project` readable.
func escapeSegment(s string, keepColon bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isPlainSegmentByte(c) || (keepColon && c == ':') {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func isPlainSegmentByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("-._~!$&'()+,;=", c) >= 0
}

// SplitQualified splits `schema.table` (or `database.table`) at the first dot
// outside quotes, keeping the quotes on each part so the target can fold what
// the database folds and keep what it keeps. quote is '"' for Postgres and
// Redshift, '`' for MySQL and BigQuery.
func SplitQualified(name string, quote byte) (qualifier, table string, qualified bool) {
	quoted := false
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case quote:
			quoted = !quoted
		case '.':
			if !quoted {
				return name[:i], name[i+1:], true
			}
		}
	}
	return "", name, false
}

// SearchPathSchema is the schema an unqualified Postgres-dialect name resolves
// to, as far as the DSN says: `search_path=…`, or `options=-c search_path=…`,
// read from the runtime parameters the driver parsed out of it. `$user` is
// skipped, because which user that is belongs to the server. With nothing set,
// `public` -- the server's default.
//
// A search_path set on the role or on the database is invisible without a
// query, and a Locator makes none. Qualify the name when that is how the
// database is set up.
func SearchPathSchema(params map[string]string) string {
	path := params["search_path"]
	if path == "" {
		opts := strings.ReplaceAll(params["options"], "-c ", "-c")
		for _, opt := range strings.Fields(opts) {
			if v, ok := strings.CutPrefix(opt, "-csearch_path="); ok {
				path = v
				break
			}
		}
	}
	for _, s := range strings.Split(path, ",") {
		s = strings.TrimSpace(s)
		if s != "" && strings.Trim(s, `"`) != "$user" {
			return s
		}
	}
	return "public"
}
