// Package autotable routes each event to the table its own envelope names, and
// creates that table when it is absent.
//
// It writes nothing. `into` names the destination that does, and every table it
// creates has the same fixed columns plus whatever the record carries -- so
// creating one is a template rather than a decision, and the same package
// serves Postgres, MySQL, BigQuery and Redshift without knowing which.
package autotable

import (
	"fmt"
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
// The operation vocabulary, from the SDK's landing layout.
const (
	OpInsert = sdk.LandingInsert
	OpUpdate = sdk.LandingUpdate
	OpDelete = sdk.LandingDelete
)

// The landing layout, which lives in the SDK.
//
// Named here too, because this package's own code and tests read them by
// these names -- and because a gateway operator reading this file should not
// have to follow an import to learn what a table gets. They are aliases, not
// copies: there is one definition, in `sdk`, and a pipeline that lands this
// layout produces the same table and the same ids as this does.
const (
	Prefix = sdk.LandingPrefix

	ColumnID            = sdk.LandingColumnID
	ColumnRecordKey     = sdk.LandingColumnRecordKey
	ColumnOperation     = sdk.LandingColumnOperation
	ColumnReceivedAt    = sdk.LandingColumnReceivedAt
	ColumnLoadedAt      = sdk.LandingColumnLoadedAt
	ColumnStream        = sdk.LandingColumnStream
	ColumnGateway       = sdk.LandingColumnGateway
	ColumnReceivedBytes = sdk.LandingColumnReceivedBytes
)

// PartitionBy is the column a created table is partitioned on, by day.
const PartitionBy = sdk.LandingPartitionBy

// ClusterBy is how a table is clustered: by the record.
var ClusterBy = sdk.LandingClusterBy()

// fixed is the part of the schema that never varies, except where the write
// mode makes it vary. See sdk.LandingSchema for what the two flags mean.
func fixed(unique, keyed bool) sdk.Schema { return sdk.LandingSchema(unique, keyed) }

// envelope is one request, taken apart.
type envelope struct {
	table string
	key   string
	op    string

	// bytes is what this event arrived as, stamped by the pipe through the
	// Measurer seam rather than read off the producer's envelope -- a
	// producer must not be able to report their own volume.
	bytes int64

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

	// Stamped by the pipe through the Measurer seam, never read off what the
	// producer sent: Measure overwrites, so a client who puts this field on
	// their envelope reports nothing but their own arrival size anyway.
	//
	// Absent is legitimate -- a unit test building an envelope by hand, a
	// path that never went through a pipe -- and leaves the column NULL,
	// which is the honest answer to "how big was it" when nobody measured.
	if n, ok := e[ColumnReceivedBytes].(int64); ok {
		env.bytes = n
	}
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
	if env.bytes > 0 {
		// Absent rather than 0 when nothing measured it: a zero sums, and a
		// column whose zeros are "we did not look" is a column that lies in
		// aggregate -- which is the only way anybody reads this one.
		row[ColumnReceivedBytes] = env.bytes
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
// identify is this record's landing id. The formula lives in the SDK, so a
// pipeline landing the same record mints the same id.
func (env envelope) identify() (string, error) {
	return sdk.LandingID(env.table, env.key, env.record)
}

// Provider is the constant stamped into every id minted here.
const Provider = sdk.LandingProvider
