package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
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

// LandingSchema is the part of the layout that never varies, except where the
// write mode makes it vary -- and both places it does are the same decision
// seen twice.
//
// brevis_loaded_at is a database DEFAULT and not a value anybody sends, and
// that is deliberate: the sender knows the DISPATCH time, the destination
// knows the WRITE time. The difference between the two columns is then the
// real end-to-end latency, per row, with no instrumentation at all.
//
// `unique` is the UNIQUE constraint on the id: required by `merge` on
// Postgres and MySQL, refused by BigQuery, wrong for `append`.
//
// `keyed` is whether brevis_record_key is NOT NULL. Under `merge` the key is
// required of every record, so the column can be. Under `append` a record may
// name none, and a NOT NULL column would then refuse the row at the database
// -- at write time, failing the batch.
//
// It does NOT include LandingColumnData. A caller composing a document-shaped
// table appends LandingDataColumn(); one composing their own columns appends
// those instead.
func LandingSchema(unique, keyed bool) Schema {
	return Schema{
		{Name: LandingColumnID, Type: TypeString, Required: true, Unique: unique},
		{Name: LandingColumnRecordKey, Type: TypeString, Required: keyed},
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
