package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// The landing table's layout: the columns every such table carries, the id
// they are keyed on, and the fingerprint that id is built from.
//
// It lives here, and not in the gateway that first needed it, for the reason
// ComputeIngestionID lives here: there has to be exactly ONE place that
// decides what a landing row looks like. A pipeline that lands this layout
// and a gateway that lands it must produce the SAME table and the SAME ids,
// or a team cannot move between them and cannot read the two together. Two
// implementations of one contract is a contract that drifts.
//
// Nothing here is a mode. LandingSchema returns columns; the caller composes
// them into Target.Schema like any others, and may append their own. The
// default is still the client declaring what the table is -- this only saves
// them typing a layout that already exists.

// LandingPrefix is reserved. A record carrying any key with it is refused by
// whoever is shaping it, because otherwise a producer forges a control field
// -- and a forged brevis_received_at is worse than none, since it looks real.
const LandingPrefix = "brevis_"

// The columns every landing table carries, whatever the record holds.
//
// ALL of them prefixed, brevis_ingestion_id included. The identity column is
// the layout's own: a producer never writes an ingestion_id and never needs
// one, and a field of theirs called that is theirs to keep.
//
// It costs WriteOptions.DedupKey, added for exactly this: every driver used
// to match on `ingestion_id` BY NAME, so the first version of this created
// tables with the right columns into which every merge refused. The
// integration test caught it -- zero rows.
const (
	LandingColumnID         = LandingPrefix + "ingestion_id"
	LandingColumnRecordKey  = LandingPrefix + "record_key"
	LandingColumnOperation  = LandingPrefix + "operation"
	LandingColumnReceivedAt = LandingPrefix + "received_at"
	LandingColumnLoadedAt   = LandingPrefix + "loaded_at"
	LandingColumnStream     = LandingPrefix + "stream"
	LandingColumnGateway    = LandingPrefix + "gateway"

	// LandingColumnReceivedBytes is how large the record was when it ARRIVED,
	// its envelope included.
	//
	// It answers what per-stream metrics structurally cannot: "which table is
	// growing, and since when". The alternative was scanning the JSON column
	// to add it up -- roughly fifty times the bytes scanned, every time
	// somebody asks.
	//
	// It is INGRESS and not storage. The destination keeps the row typed and
	// compressed, so 400 bytes of JSON may be 80 on disk: summing this gives
	// what arrived, never what is billed for keeping it.
	LandingColumnReceivedBytes = LandingPrefix + "received_bytes"

	// LandingColumnData holds the record whole, as JSON.
	//
	// Unprefixed, because it is the only column here that is the PRODUCER's:
	// the eight above are the layout's, and this one is what they are about.
	LandingColumnData = "data"
)

// The values LandingColumnOperation takes.
//
// UPDATE and DELETE are RECORDED, never applied. A landing table is history,
// and resolving the current version is the downstream model's job.
const (
	LandingInsert = "INSERT"
	LandingUpdate = "UPDATE"
	LandingDelete = "DELETE"
)

// LandingPartitionBy is the column a landing table is partitioned on, by day.
//
// The RECEIVER's clock, never the producer's. A producer's can be wrong or
// absent, and a partition column a producer controls is a producer that can
// write into 2035. An unpartitioned landing table is a query bill that grows
// forever, so this is not optional.
const LandingPartitionBy = LandingColumnReceivedAt

// LandingClusterBy is how a landing table is clustered: by the record,
// because looking up one record's history is what anybody does with one.
//
// A function and not a package variable: a slice anybody can reach is a slice
// anybody can append to, and this one is read by every table that gets
// created.
func LandingClusterBy() []string { return []string{LandingColumnRecordKey} }

// LandingOptions is what the write mode makes vary, and both fields are the
// same decision seen twice.
type LandingOptions struct {
	// UniqueID puts a UNIQUE constraint on brevis_ingestion_id.
	//
	// Required by `merge` on Postgres and MySQL, refused by BigQuery, and
	// wrong for `append` -- where the same record legitimately lands twice
	// and the history is the point.
	UniqueID bool

	// Keyed makes brevis_record_key NOT NULL.
	//
	// Under `merge` the key is required of every record, so the column can
	// be. Under `append` a record may name none, and a NOT NULL column would
	// then refuse the row at the database -- at write time, failing the batch
	// around it.
	Keyed bool
}

// LandingSchema is the landing table, ready to compose:
//
//	Target{
//		To:     to.Postgres(dsn, "landing.orders"),
//		Schema: sdk.LandingSchema(sdk.LandingOptions{Keyed: true}),
//	}
//
// Nine columns: the eight the layout owns and the one the producer does. The
// caller may append their own, and the ORDER is the DDL's -- a table created
// from this has to match one a gateway created, column for column.
//
// It is not a mode. Target.Schema is still the caller declaring what the
// table is; this only saves them typing a layout that already exists, and
// CreationPlan never learns that anything special happened.
func LandingSchema(o LandingOptions) Schema {
	return append(LandingControlColumns(o), LandingDataColumn())
}

// LandingControlColumns is the eight columns the layout owns, without the
// producer's.
//
// For a caller giving the record's fields columns of their own instead of
// landing it whole as JSON. They append theirs; these stay first, in this
// order.
//
// brevis_loaded_at is a database DEFAULT and not a value anybody sends, and
// that is deliberate: the sender knows the DISPATCH time, the destination
// knows the WRITE time. The difference between the two columns is then the
// real end-to-end latency, per row, with no instrumentation at all.
func LandingControlColumns(o LandingOptions) Schema {
	return Schema{
		{Name: LandingColumnID, Type: TypeString, Required: true, Unique: o.UniqueID},
		{Name: LandingColumnRecordKey, Type: TypeString, Required: o.Keyed},
		{Name: LandingColumnOperation, Type: TypeString, Required: true},
		{Name: LandingColumnReceivedAt, Type: TypeTimestamp, Required: true},
		{Name: LandingColumnLoadedAt, Type: TypeTimestamp, Default: CurrentTimestamp},
		{Name: LandingColumnStream, Type: TypeString},
		{Name: LandingColumnGateway, Type: TypeString},
		// Nullable: a row written before this column existed has no answer,
		// and zero would be a lie that sums.
		{Name: LandingColumnReceivedBytes, Type: TypeInt64},
	}
}

// LandingDataColumn is the one column a document-shaped landing table adds:
// the record, whole, as JSON.
//
// The same one column whatever the record holds, which is what makes a table
// created this way a template rather than a decision.
func LandingDataColumn() Column {
	return Column{Name: LandingColumnData, Type: TypeJSON}
}

// LandingProvider is the constant stamped into every landing id.
//
// It is a FORMAT namespace and not a feature name, and it must not be
// "corrected" to match whatever is calling it: every id already written is
// built from this string, and changing it changes all of them. The next merge
// after that duplicates the table.
const LandingProvider = "auto_table"

// LandingID is the deterministic id of one landing row.
//
//	uuid5(ns, "auto_table | table | key | sha256(canonical(record))")
//
// Content-addressed through the last slot: the same record gets the same id
// from anywhere, and a changed record gets a new one. That is what lets a
// pipeline and a gateway land the same row twice without duplicating it.
func LandingID(table, key string, record map[string]any) (string, error) {
	sum, err := LandingFingerprint(record)
	if err != nil {
		return "", err
	}
	// With no record key -- which only `append` allows -- the content IS the
	// key. The slot cannot be left empty: Envelope.IngestionID refuses that,
	// and rightly, because an id computed over a blank identity is an id
	// every keyless record in the table would share.
	//
	// It cannot collide with a keyed record either. A keyed one puts the
	// producer's value in this slot and the fingerprint in the next; for the
	// two to meet, a record key would have to be literally "sha256:<64 hex>"
	// AND the record's own fingerprint.
	if key == "" {
		key = "sha256:" + sum
	}
	e := Envelope{
		Provider:  LandingProvider,
		Entity:    table,
		SourceKey: key,
		RecordTS:  "sha256:" + sum,
	}
	return e.IngestionID()
}

// LandingFingerprint is a sha256 over the record in CANONICAL form: keys
// sorted at every level, arrays left in order, everything quoted.
//
// Canonical because Go's map iteration is randomised, so a plain json.Marshal
// of a map produces a different byte string on every call -- and an id built
// on that would differ between two deliveries of the same record, which is
// the one thing it exists to prevent.
func LandingFingerprint(record map[string]any) (string, error) {
	var b strings.Builder
	if err := canonicalJSON(&b, record); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			q, err := json.Marshal(k)
			if err != nil {
				return err
			}
			b.Write(q)
			b.WriteByte(':')
			if err := canonicalJSON(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
		return nil

	case []any:
		// An array's ORDER is significant and is not sorted: [1,2] and [2,1]
		// are different documents, and collapsing them would merge two
		// records that are not the same one.
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonicalJSON(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	}

	enc, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b.Write(enc)
	return nil
}

// LandingArg configures Landing.
type LandingArg func(*landing)

type landing struct {
	table   string
	key     string // the field naming the record's key; empty is keyless
	op      string // the field naming the operation; empty is always INSERT
	columns bool   // one column per field, instead of the record whole in `data`
}

// LandingKey names the field that identifies the record.
//
// It becomes brevis_record_key and the third slot of the id, so two
// deliveries of the same record meet. Without it the record is keyless and
// its CONTENT is its key -- which is what `append` allows and `merge` does
// not, because a merge with nothing to match on matches nothing.
func LandingKey(field string) LandingArg { return func(l *landing) { l.key = field } }

// LandingOperationFrom names the field carrying INSERT, UPDATE or DELETE.
//
// For a source that computes them -- a CDC feed, a soft-delete column. The
// verb is RECORDED and never applied: a landing table is history, and
// resolving the current version is the downstream model's job.
//
// Without it every row is INSERT, which is what a source that only ever adds
// is honestly saying.
func LandingOperationFrom(field string) LandingArg { return func(l *landing) { l.op = field } }

// LandingColumns gives each of the record's fields a column of its own,
// instead of the record whole in `data`.
//
//	sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
//
// It is the shape a gateway lands as `shape: columns`, and it is now literally
// the same code: the same record spread by a pipeline and by a gateway gives
// the same columns with the same values, so the two can be read together.
//
// The table is then LandingControlColumns plus the caller's own columns, NOT
// LandingSchema -- that one carries `data`. The SDK still never infers a
// schema: this option decides how the ROW is built and never what the table
// is. The fields are declared by the caller, in the YAML or in Go, reviewed in
// a diff.
//
// Scalars become text, objects and arrays become JSON under the key that held
// them, and null stays NULL. It does not flatten: a record that wants one row
// per array element wants ArrayAt, which is a different operation with a
// different name.
//
// One thing worth knowing before choosing it: in this shape `data` is not the
// layout's column any more, so a producer with a field called `data` -- which
// is exactly what the Bacen series sends -- gets a column of their own with
// their own value in it. In the document shape theirs would be one key inside
// the layout's JSON.
func LandingColumns() LandingArg { return func(l *landing) { l.columns = true } }

// Landing turns a record into a landing row: the layout's control columns,
// plus the record itself in the `data` column.
//
//	sdk.Run(sdk.Pipeline{
//		Source:    /* ... */,
//		Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
//		Target: sdk.Target{
//			To:     to.Postgres(dsn, table),
//			Schema: sdk.LandingSchema(sdk.LandingOptions{Keyed: true}),
//		},
//	})
//
// The row it composes is exactly what LandingSchema declares, and what a
// gateway lands for the same record — the same columns and the same
// brevis_ingestion_id, so the two can be read together and a team can move
// between them without a migration.
//
// `table` is the id's second slot, so it must be the table this pipeline
// actually writes to. NOTHING CHECKS THAT. The Writer knows its own table but
// exposes it only through Describe, which is documented as the name "for logs
// and errors" — building the id on that would tie every id already written to
// a log string. So the name is passed here and declared again in `To`, and
// the way to keep them honest is to write it once:
//
//	const table = "landing.orders"
//
// Get it wrong and the rows land correctly and the ids are minted for a table
// nobody wrote to. They will look fine. They will not match the gateway's.
func Landing(table string, opts ...LandingArg) Transformer {
	l := &landing{table: table}
	for _, o := range opts {
		o(l)
	}
	return func(payload any) (any, error) {
		record, ok := payload.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Landing needs a JSON object, got %T", payload)
		}
		// The prefix is reserved, and this REFUSES rather than overwrites.
		// The gateway overwrites because its producer is a stranger who must
		// not be able to forge a control field; here the producer is the
		// pipeline's own author, and replacing what they wrote would hide
		// their mistake instead of naming it.
		for k := range record {
			if strings.HasPrefix(k, LandingPrefix) {
				return nil, fmt.Errorf("the record carries %q, and %q is reserved "+
					"for the landing layout's own columns", k, LandingPrefix)
			}
		}

		var key string
		if l.key != "" {
			key = asText(record[l.key])
		}
		op := LandingInsert
		if l.op != "" {
			op = asText(record[l.op])
			switch op {
			case LandingInsert, LandingUpdate, LandingDelete:
			default:
				return nil, fmt.Errorf("field %q says the operation is %q, and the "+
					"landing layout knows %s, %s and %s: a history is only readable "+
					"if the verb set is closed",
					l.op, op, LandingInsert, LandingUpdate, LandingDelete)
			}
		}

		// Over the PRODUCER's record, before a control column is added: the
		// gateway fingerprints `data`, and an id over anything else would
		// not meet it.
		id, err := LandingID(l.table, key, record)
		if err != nil {
			return nil, err
		}

		// LandingColumnLoadedAt is ABSENT, not nil. An explicit NULL
		// OVERRIDES the column's DEFAULT, and the difference between
		// received_at and loaded_at is the end-to-end latency only if the
		// destination sets the second one. The gateway omits it for exactly
		// this reason; sending nil here made the pipeline's rows land with
		// a null where the gateway's have a timestamp, and only the
		// integration test saw it -- the unit test asserted on the map this
		// function builds, which is not what reaches the table.
		row := map[string]any{
			LandingColumnID:        id,
			LandingColumnOperation: op,
			// The SDK's own clock, per record, and not the caller's: a value
			// from outside would turn "when this was taken in" into
			// something else with the same name, and the partitioning
			// assumes the first meaning. IngestionLoadedAt says the same.
			LandingColumnReceivedAt: time.Now().UTC().Format(time.RFC3339),
			// The gateway's two. NULL is the honest answer and the readable
			// signal: a row with brevis_gateway IS NULL did not come through
			// one.
			LandingColumnStream:  nil,
			LandingColumnGateway: nil,
			// NULL, and it is the same decision as the two above rather than
			// an oversight. The gateway's number is what arrived ON THE
			// WIRE, envelope included; the record's own JSON here is about
			// 30% smaller for a typical event. Filling it would give a
			// column whose job is volume trend two meanings with one name,
			// and summing across rows from both paths would be wrong by
			// whatever share came from which. `length(data)` answers the
			// pipeline's version exactly, without pretending.
			LandingColumnReceivedBytes: nil,
		}
		if key != "" {
			row[LandingColumnRecordKey] = key
		} else {
			row[LandingColumnRecordKey] = nil
		}

		if !l.columns {
			body, err := json.Marshal(record)
			if err != nil {
				return nil, fmt.Errorf("the record is not JSON: %w", err)
			}
			row[LandingColumnData] = string(body)
			return row, nil
		}

		// One column per field, and it cannot collide with a control column:
		// every one of those carries the reserved prefix, and a record
		// carrying it was refused above. So this writes only into names
		// nothing else in the row owns.
		//
		// AFTER the id, never before. The id is content-addressed over the
		// PRODUCER's record -- where 8.89 is a number and a nested object is
		// an object. Fingerprinting the spread instead, where both have
		// become text, would give the same record two ids depending on the
		// shape it is stored in, and the next merge duplicates the table.
		spread, err := LandingSpread(record)
		if err != nil {
			return nil, err
		}
		for k, v := range spread {
			row[k] = v
		}
		return row, nil
	}
}

// LandingFieldName is what a record's field has to match to become a column
// of its own.
//
// BigQuery's rule, which is the narrowest of the four destinations: a letter
// or underscore, then letters, digits and underscores. Postgres would accept
// almost anything quoted -- which is exactly the trap, because the table
// would be created there and the same fetcher would break the day somebody
// points it at BigQuery.
var LandingFieldName = core.ColumnName

// LandingFieldNames refuses a record this layout cannot turn into columns.
//
// Separate from LandingSpread so a caller holding one record can ask before
// a whole batch is shaped: the gateway checks per event, before anything is
// buffered, because one bad field name would otherwise fail the batch around
// it and send three other producers' events to the dead letter.
func LandingFieldNames(record map[string]any) error {
	for _, k := range landingKeys(record) {
		if err := core.CheckColumnName(k); err != nil {
			return err
		}
	}
	return nil
}

// LandingSpread turns a record into the columns it contributes: each field
// its own column, objects and arrays as JSON, null as NULL.
//
// It does NOT flatten. A nested object becomes one JSON column under the
// key that held it, which is what the gateway's `shape: columns` does too --
// a record that needs one row per element wants ArrayAt, which is a
// different operation with a different name.
//
// The rendering is the SDK's own, and deliberately not the gateway's `Text`.
// That one is documented as free to get stricter; these values are WRITTEN
// INTO TABLES, so they are as unchangeable as the ids are. A renderer that
// may change cannot be the one producing stored column values.
func LandingSpread(record map[string]any) (map[string]any, error) {
	if err := LandingFieldNames(record); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(record))
	for _, k := range landingKeys(record) {
		v, err := landingValue(record[k])
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

// LandingSchemaOf is the columns a record declares, and their types.
//
// The companion to LandingSpread: that one renders the row, this one declares
// the table, and the two are over the same record so they cannot disagree
// about what it contributes.
//
//	scalar, and null   → TypeString
//	object, array      → TypeJSON
//
// Sorted, so the same record always declares the same DDL. Unsorted, two runs
// over identical data would produce columns in different orders, and a CREATE
// TABLE is easier to reason about when it does not move.
//
// THE SHAPE DECIDES, NEVER THE VALUE, and that is what keeps this on the
// right side of invariant I2. Reading the value looks reasonable -- 21129 IS
// an integer -- and it is the exact failure the SDK refuses everywhere else:
// a field that arrives whole today and fractional tomorrow would change the
// column's type with nobody writing anything, and the row that no longer fits
// goes to the dead letter. `true` is the case that most invites an exception
// and does not get one.
//
// What it costs, said where somebody will read it: no partition pruning on a
// date inside the record, no numeric aggregation without a cast. Typing a
// column is the PROMOTION path -- a human writing it down, reviewed in a diff.
func LandingSchemaOf(record map[string]any) (Schema, error) {
	if err := LandingFieldNames(record); err != nil {
		return nil, err
	}
	out := make(Schema, 0, len(record))
	for _, k := range landingKeys(record) {
		out = append(out, Column{Name: k, Type: core.TypeFromShape(record[k])})
	}
	return out, nil
}

func landingValue(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any, []any:
		body, err := json.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("not JSON: %w", err)
		}
		return string(body), nil
	case nil:
		// NULL and not "": a field the producer sent as null and a field
		// they sent as an empty string are different facts.
		return nil, nil
	}
	return asText(v), nil
}

func landingKeys(record map[string]any) []string {
	out := make([]string, 0, len(record))
	for k := range record {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
