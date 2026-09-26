// Package autotable routes each event to the table its own envelope names, and
// creates that table when it is absent.
//
// It writes nothing. `into` names the destination that does, and every table it
// creates has the same fixed columns plus whatever the record carries -- so
// creating one is a template rather than a decision, and the same package
// serves Postgres, MySQL, BigQuery and Redshift without knowing which.
package autotable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// Sink is what the YAML calls this driver.
const Sink = gateway.SinkAutoTable

// The envelope. Control fields at the top, the record inside `data`.
//
// The split is the whole point: `table_name` sitting beside `total` and
// `customer` was always wrong -- it is a field of the TRANSPORT and not of the
// record -- and separating them is how every CDC format is shaped.
const (
	FieldTable       = "table_name"
	FieldData        = "data"
	FieldUniqueKey   = "unique_key"
	FieldOperation   = "operation"
	FieldDescription = "description"
)

// DefaultUniqueKey is the field inside `data` that identifies the record when
// the envelope names no other.
//
// Whether it has to be THERE depends on the write mode, and that is the whole
// of the rule:
//
//	merge   required. The mode exists to hold one row per record, and
//	        `brevis_record_key` is what "per record" means -- the qualify
//	        that resolves the current version partitions by it. A merge
//	        table full of rows that name no record is a table nobody can
//	        resolve.
//	append  optional. An append table is a log, and a log entry does not
//	        have to be about a record: an audit line, a webhook, a metric
//	        sample. Demanding an `id` there would make producers invent one,
//	        which is worse than NULL because it looks real.
//
// Naming a field that is not in `data` is an error in BOTH modes. "You did not
// say" and "you said something that is not there" are different mistakes, and
// only the first one is allowed to pass.
const DefaultUniqueKey = "id"

// The operations a landing table records.
//
// RECORDS, and does not apply. A landing table is history: an UPDATE applied in
// place loses the previous version, and the day you want it is the day
// something broke. Resolving "the current version of each record" is the
// downstream model's job:
//
//	qualify row_number() over (partition by brevis_record_key
//	                           order by brevis_received_at desc) = 1
//	   and brevis_operation <> 'DELETE'
//
// Which is what Debezium, Fivetran and Airbyte all do -- and it means
// `operation` costs nothing in the write path: no upsert mode, no lock, no
// delete.
const (
	OpInsert = "INSERT"
	OpUpdate = "UPDATE"
	OpDelete = "DELETE"
)

// Prefix is reserved. A `data` carrying any key with it is refused, because
// otherwise a producer forges a control field -- and a forged
// brevis_received_at is worse than none, since it looks real.
const Prefix = "brevis_"

// The columns every table carries, whatever the record holds.
//
// ALL of them prefixed, `brevis_ingestion_id` included. The identity column is
// this gateway's own here: a producer posting an envelope never writes an
// `ingestion_id` and never needs one, and a field of theirs called that is
// theirs to keep.
//
// It costs `WriteOptions.DedupKey` in the SDK, added for exactly this: every
// driver used to match on `ingestion_id` BY NAME, so the first version of this
// created tables with the right columns into which every merge refused. The
// integration test caught it -- zero rows.
const (
	ColumnID         = Prefix + "ingestion_id"
	ColumnRecordKey  = Prefix + "record_key"
	ColumnOperation  = Prefix + "operation"
	ColumnReceivedAt = Prefix + "received_at"
	ColumnLoadedAt   = Prefix + "loaded_at"
	ColumnStream     = Prefix + "stream"
	ColumnGateway    = Prefix + "gateway"
)

// PartitionBy is the column a created table is partitioned on, by day.
//
// OUR clock, never a client's. A producer's can be wrong or absent, and a
// partition column a client controls is a client that can write into 2035. An
// unpartitioned landing table is a query bill that grows forever, so this is
// not optional.
const PartitionBy = ColumnReceivedAt

// ClusterBy is how a table is clustered: by the record, because looking up one
// record's history is what anybody does with a landing table.
var ClusterBy = []string{ColumnRecordKey}

// fixed is the part of the schema that never varies.
//
// `brevis_loaded_at` is a database DEFAULT and not a value we send, and that is
// deliberate: the gateway knows the DISPATCH time, the destination knows the
// WRITE time. The difference between the two columns is then the real
// end-to-end latency, per row, with no instrumentation at all.
// fixed is the part of the schema that never varies, except where the write
// mode makes it vary -- and both places it does are the same decision seen
// twice.
//
// `unique` is the UNIQUE constraint on the id: required by `merge` on Postgres
// and MySQL, refused by BigQuery, wrong for `append`.
//
// `keyed` is whether `brevis_record_key` is NOT NULL. Under `merge` the key is
// required of every event, so the column can be. Under `append` a record may
// name none, and a NOT NULL column would then refuse the row at the database
// -- at write time, failing the batch, which is the failure this whole file
// spent a version learning to avoid.
func fixed(unique, keyed bool) sdk.Schema {
	return sdk.Schema{
		{Name: ColumnID, Type: sdk.TypeString, Required: true, Unique: unique},
		{Name: ColumnRecordKey, Type: sdk.TypeString, Required: keyed},
		{Name: ColumnOperation, Type: sdk.TypeString, Required: true},
		{Name: ColumnReceivedAt, Type: sdk.TypeTimestamp, Required: true},
		{Name: ColumnLoadedAt, Type: sdk.TypeTimestamp, Default: sdk.CurrentTimestamp},
		{Name: ColumnStream, Type: sdk.TypeString},
		{Name: ColumnGateway, Type: sdk.TypeString},
	}
}

// envelope is one request, taken apart.
type envelope struct {
	table string
	key   string
	op    string

	// desc is READ AND NOT USED, and that is worth saying rather than leaving
	// for somebody to discover: the envelope carries `description` for the
	// console's ingestion page, which does not exist. It is validated here so
	// the field means the same thing on the day something reads it, and the
	// docs say plainly that it goes nowhere today -- a field accepted in
	// silence is a field somebody believes is being stored.
	desc string

	record map[string]any
}

// open reads the envelope and refuses what it cannot honour.
//
// Every refusal here is per EVENT: the producer gets it in the response, at the
// moment they can still fix it, and every well-formed event in the same request
// still lands.
func open(e map[string]any) (envelope, error) {
	var env envelope

	env.table = gateway.Text(e[FieldTable])
	if env.table == "" {
		return env, fmt.Errorf("no %q, and that field is what says which table this "+
			"belongs in", FieldTable)
	}

	record, ok := e[FieldData].(map[string]any)
	if !ok {
		if _, present := e[FieldData]; !present {
			return env, fmt.Errorf("no %q: the record goes inside it, and the fields "+
				"beside it are the envelope", FieldData)
		}
		return env, fmt.Errorf("%q is not a JSON object", FieldData)
	}
	if len(record) == 0 {
		return env, fmt.Errorf("%q is empty", FieldData)
	}
	env.record = record

	// The reserved prefix, checked before anything reads the record: a forged
	// brevis_received_at is worse than a missing one, because it looks real.
	for k := range record {
		if strings.HasPrefix(k, Prefix) {
			return env, fmt.Errorf("%q.%q uses the reserved %q prefix, which names "+
				"the gateway's own columns", FieldData, k, Prefix)
		}
	}

	// The key, and the two ways it can be absent are not the same thing.
	//
	// A field the envelope NAMED and `data` does not carry is a mistake in
	// either write mode: the producer said `pedido_id` and there is no
	// `pedido_id`, so accepting it would land a row whose record key is not
	// the one they asked for. The default `id` simply not being there is the
	// other case -- nobody said anything, and whether that is allowed is the
	// write mode's answer, given by the router.
	name := gateway.Text(e[FieldUniqueKey])
	named := name != ""
	if !named {
		name = DefaultUniqueKey
	}
	env.key = gateway.Text(record[name])
	if env.key == "" && named {
		return env, fmt.Errorf("%q names %q as this record's identity and %q.%q is "+
			"missing or empty. Drop %q to fall back to %q, or send the field",
			FieldUniqueKey, name, FieldData, name, FieldUniqueKey, DefaultUniqueKey)
	}

	env.op = strings.ToUpper(gateway.Text(e[FieldOperation]))
	switch env.op {
	case "":
		env.op = OpInsert
	case OpInsert, OpUpdate, OpDelete:
	default:
		return env, fmt.Errorf("%q is %q (use %s, %s or %s)",
			FieldOperation, env.op, OpInsert, OpUpdate, OpDelete)
	}

	env.desc = gateway.Text(e[FieldDescription])
	return env, nil
}

// row builds the columns the gateway owns. What the record contributes is the
// shape's job -- see shape.go's document and columns modes.
func (env envelope) row(now time.Time, stream, name string) (map[string]any, error) {
	id, err := env.identify()
	if err != nil {
		return nil, err
	}
	row := map[string]any{
		ColumnID:         id,
		ColumnOperation:  env.op,
		ColumnReceivedAt: now.UTC().Format(time.RFC3339),
		ColumnStream:     stream,
		ColumnGateway:    name,
		// ColumnLoadedAt is absent on purpose: the destination's DEFAULT
		// stamps it, which is the only clock that knows when the write
		// actually happened.
	}
	if env.key != "" {
		// Absent rather than "", which is the difference between NULL and a
		// record whose key is the empty string. Under `append` a record may
		// legitimately name none, and the column is nullable there; under
		// `merge` the router has already refused the event.
		row[ColumnRecordKey] = env.key
	}
	return row, nil
}

// identify computes the event's id.
//
//	uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
//
// No clock. `occurred_at` used to be a client's field and part of this formula;
// making arrival OUR responsibility would have put time.Now() in here, and
// time.Now() differs on every delivery -- so every retry would have been a new
// event and `merge` would have stopped absorbing anything.
//
// The fingerprint replaces it and is better for CDC:
//
//	same record, same content, twice   → same id, merge absorbs
//	same record, content changed       → different id, both versions land
//
// The caveat is the documented one: two genuinely distinct events with
// byte-identical `data` collapse. For CDC that is CORRECT -- two identical
// updates to one record with the same values are the same fact.
func (env envelope) identify() (string, error) {
	sum, err := fingerprint(env.record)
	if err != nil {
		return "", err
	}
	// With no record key -- which only `append` allows -- the content IS the
	// key. The slot cannot be left empty: `Envelope.IngestionID` refuses that,
	// and rightly, because an id computed over a blank identity is an id every
	// keyless record in the table would share.
	//
	// It cannot collide with a keyed record either. A keyed one puts the
	// producer's value in this slot and the fingerprint in the next; for the
	// two to meet, a record key would have to be literally "sha256:<64 hex>"
	// AND the record's own fingerprint.
	key := env.key
	if key == "" {
		key = "sha256:" + sum
	}
	e := sdk.Envelope{
		Provider:  Provider,
		Entity:    env.table,
		SourceKey: key,
		RecordTS:  "sha256:" + sum,
	}
	return e.IngestionID()
}

// Provider is the constant stamped into every id, so an id minted here can
// never collide with one a declared stream minted for the same table and key.
const Provider = "auto_table"

// fingerprint is a sha256 over the record in CANONICAL form: keys sorted at
// every level, arrays left in order, everything quoted.
//
// Canonical because Go's map iteration is randomised, so a plain json.Marshal
// of a map produces a different byte string on every call -- and an id built on
// that would differ between two deliveries of the same event, which is the one
// thing it exists to prevent.
func fingerprint(record map[string]any) (string, error) {
	var b strings.Builder
	if err := canonical(&b, record); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

func canonical(b *strings.Builder, v any) error {
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
			if err := canonical(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
		return nil

	case []any:
		// An array's ORDER is significant and is not sorted: [1,2] and [2,1]
		// are different documents, and collapsing them would merge two events
		// that are not the same one.
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonical(b, e); err != nil {
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
