package core

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

// ColumnName is what a field has to match to become a column of its own.
//
// BigQuery's rule, which is the narrowest of the four destinations: a letter
// or underscore, then letters, digits and underscores. Postgres would accept
// almost anything quoted -- which is exactly the trap, because the table
// would be created there and the same fetcher would break the day somebody
// points it at BigQuery.
var ColumnName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// CheckColumnName refuses a field this layout cannot turn into a column.
func CheckColumnName(name string) error {
	if ColumnName.MatchString(name) {
		return nil
	}
	return fmt.Errorf("the field %q cannot be a column name: it has to match %s. "+
		"That is BigQuery's rule and it is the narrowest of the four, so a name "+
		"that passes works everywhere", name, ColumnName)
}

// JSONText is text that is already JSON, and says so.
//
// It exists because the shape of a value has to survive being rendered. A
// pipeline renders in its TRANSFORMER -- an object becomes `{"uf":"SP"}`
// before the row reaches any destination -- so by the time the driver looks
// at it there is nothing left to tell it apart from a producer's ordinary
// string. The gateway does not have that problem: it types from the RAW
// record and renders separately.
//
// The consequence was not cosmetic. The gateway declared `meta JSON` and a
// pipeline declared `meta STRING` for the same record, so on a SHARED table
// the second writer was refused outright -- "meta is json in the table and
// string in the declaration" -- and a pipeline could not write into a
// gateway's `shape: columns` table, which is the one interoperability the
// landing layout exists for.
//
// A MARKER and not inference. The alternative considered was reading the
// string back: does it parse as an object or an array? That is the invariant
// I2 line -- a producer sending the literal text `[1,2]` would get a JSON
// column nobody asked for. This carries forward a decision already made,
// where it was made, on the record whose shape was still visible.
//
// It renders as its own text everywhere: its underlying kind is string, which
// is what database/sql's default converter and pgx both read, and what
// encoding/json quotes.
type JSONText string

// MarshalJSON writes the text as JSON rather than as a string of it.
//
// Without this the marker is worse than nothing. A driver encoding a row puts
// a plain string in quotes and escapes it, so a JSON column receives
//
//	"{\"uf\":\"SP\"}"
//
// which is a valid JSON STRING, stored without complaint, and every
// JSON_EXTRACT against it afterwards returns NULL. Measured on MySQL: with a
// map the column holds {"uf": "SP"} and JSON_EXTRACT finds SP; with the text
// it holds the escaped form and finds nothing. The column is the right type
// and the data in it is unreachable, which is the worst of the three
// outcomes because it looks correct.
//
// encoding/json compacts and validates whatever a Marshaler returns, so text
// that is not JSON fails here by name rather than corrupting the document.
func (j JSONText) MarshalJSON() ([]byte, error) { return []byte(j), nil }

// TypeFromShape is the whole of the type rule.
//
//	scalar, and null       → TypeString
//	object, array          → TypeJSON
//	JSONText               → TypeJSON, because it was one
//
// THE SHAPE DECIDES, NEVER THE VALUE, and that is what keeps this on the right
// side of invariant I2. Reading the value looks reasonable -- 21129 IS an
// integer -- and it is the exact failure the SDK refuses everywhere else: a
// field that arrives whole today and fractional tomorrow would change the
// column's type with nobody writing anything, and the row that no longer fits
// goes to the dead letter. `true` is the case that most invites an exception
// and does not get one.
//
// What it costs, said where somebody will read it: no partition pruning on a
// date inside the record, no numeric aggregation without a cast. Typing a
// column is the PROMOTION path -- a human writing it down, reviewed in a diff.
func TypeFromShape(v any) ColumnType {
	switch v.(type) {
	case map[string]any, []any:
		return TypeJSON
	case JSONText:
		// Already rendered, by whoever could still see it was an object.
		return TypeJSON
	}
	return TypeString
}

// CheckDiscoveryHasADeclaration refuses EvolveAdditiveFromPayload with
// nothing to complete.
//
// The mode COMPLETES a declaration from the batch. With no declaration it
// becomes "create the whole table from the payload", which is a different
// decision with a different name -- and the one CreationPlan refuses in a
// message this would walk straight past.
//
// The harm is concrete, and it is the landing layout. The row carries
// brevis_received_at, so the table would take it as text instead of a
// timestamp; and brevis_loaded_at is ABSENT from the row, because it is a
// database DEFAULT, so it would not be created at all. The end-to-end latency
// measurement would be gone, in a table that looks right.
//
// How MUCH is declared is the consumer's business. That there IS a
// declaration is the SDK's.
func CheckDiscoveryHasADeclaration(mode Evolution, declared []string, table string) error {
	if !mode.FromPayload() || len(declared) > 0 {
		return nil
	}
	return fmt.Errorf("%s is set to evolve from the payload and nothing is "+
		"declared. That mode COMPLETES a declaration with the columns a batch "+
		"carries; with nothing to complete it would create the whole table "+
		"from the payload, which this SDK does not do. Declare Target.Schema "+
		"-- sdk.LandingControlColumns(...) is the landing layout's half of it "+
		"-- or use sdk.EvolveAdditive, which adds only what you declared",
		table)
}

// Discovered is what the batch carries and the declaration does not.
//
// Over the UNION of the batch, never the first record. CheckRow looks at
// records[0] and that is enough for what it does -- every record comes out of
// one Transform chain, so the SHAPE of the chain is the same for all of them.
// This is a different question: a batch holds N records for one table and they
// need not carry the same FIELDS, and a field that appears only in the last
// one is still a column the table needs. Miss it and Reconcile refuses the
// batch at the destination, naming a field nobody declared.
//
// Sorted, so the same batch always declares the same DDL.
// It takes the WHOLE declaration rather than a list of names, and that is not
// a convenience. [#42] Three call sites used to pass `opt.Columns`, which is
// empty whenever a caller declares with a Schema -- so the mode refused them
// for "nothing is declared", naming the one thing they had done. Taking the
// options removes the opportunity rather than fixing three instances of it.
func Discovered(opt WriteOptions, records []Envelope) (Schema, error) {
	if len(records) == 0 {
		return nil, nil
	}
	declared := opt.DeclaredColumns()
	// Checked here as well as before the extract, because not every caller
	// goes through CheckDestination: the drivers are exported, and the
	// gateway calls Write directly. The one before the extract exists to
	// spend no source quota; this one exists so the rule cannot be walked
	// around.
	if err := CheckDiscoveryHasADeclaration(EvolveAdditiveFromPayload, declared, "this destination"); err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(declared))
	for _, c := range declared {
		have[c] = true
	}

	found := map[string]any{}
	for _, e := range records {
		row, err := AsObject(e.Payload)
		if err != nil {
			// Not an object is not this function's error to report: CheckRow
			// says it, with the message it has always said it with.
			continue
		}
		for k, v := range row {
			if have[k] {
				continue
			}
			// The first record that carries the field decides its type, and
			// with a rule that reads the SHAPE that is not a race: two records
			// carrying the same field under different shapes is the drift
			// case, and the column it lands in was decided by whichever
			// arrived first anyway.
			//
			// EXCEPT A NULL, WHICH IS NOT AN ANSWER. [#42] A nil has no shape,
			// so the rule has nothing to read and TypeFromShape falls through
			// to text -- and the batch could not correct it, because the first
			// record had already decided. That is how `vehicle_fuel` became
			// STRING from 786 nulls and refused the 62 arrays. So a nil is
			// held, not kept: the first record with a VALUE decides, wherever
			// it is in the batch, and among the records that do carry one the
			// first still wins.
			//
			// Nil THROUGHOUT declares nothing at all, and the rows keep their
			// key: RowFields leaves it out at the destination, because a null
			// and an absent field land the same NULL and neither is written
			// anywhere. An earlier version kept the column as text, on the
			// reasoning that dropping it would leave a row carrying a column
			// the table lacks -- which is true, and is the destination's
			// question, not this one's.
			if cur, seen := found[k]; !seen || cur == nil {
				found[k] = v
			}
		}
	}

	names := make([]string, 0, len(found))
	for k, v := range found {
		// No record gave it a value, so there is no shape to read and the
		// type would be a guess. [#42]
		if v == nil {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)

	out := make(Schema, 0, len(names))
	for _, k := range names {
		// Checked HERE and not only in the landing transformer: nothing makes
		// this mode landing-only, and on a raw pipeline `meu-campo` would
		// reach the DDL. Postgres quotes it and creates it, and the same
		// fetcher breaks the day somebody points it at BigQuery.
		//
		// A name the declaration ALREADY has is not judged: it is the
		// consumer's own column, in their own table, and this rule is about
		// what the SDK is willing to CREATE.
		if err := CheckColumnName(k); err != nil {
			return nil, err
		}
		out = append(out, Column{
			Name: k, Type: TypeFromShape(found[k]),
			// Dated here, where the column is decided on. It answers the one
			// question a reader has six months later, and it separates "a
			// batch brought this" from "somebody declared this", which
			// changes what they should do about it.
			Note: fmt.Sprintf("brevis: added from a batch on %s; the type is "+
				"the landing rule (scalar text, object and array json), not a decision",
				time.Now().UTC().Format("2006-01-02")),
		})
	}
	return out, nil
}

// WithDiscovered returns opt with the discovered columns added.
//
// It COPIES both slices, and that is the whole of why it is a function rather
// than two appends at the call site. WriteOptions travels by value, so
// extending `opt` is already per-call -- but Schema and Columns are slices,
// and append writes into the caller's backing array whenever there is room.
// Two Targets built from one LandingControlColumns would then clobber each
// other, which is the failure TestALandingSchemaComposes exists for.
func WithDiscovered(opt WriteOptions, found Schema) WriteOptions {
	if len(found) == 0 {
		return opt
	}

	schema := make(Schema, 0, len(opt.Schema)+len(found))
	schema = append(schema, opt.Schema...)
	schema = append(schema, found...)

	columns := make([]string, 0, len(opt.Columns)+len(found))
	columns = append(columns, opt.Columns...)
	names := make([]string, 0, len(found))
	for _, c := range found {
		columns = append(columns, c.Name)
		names = append(names, c.Name)
	}

	opt.Schema = schema
	opt.Columns = columns
	opt.Discovered = names
	return opt
}
