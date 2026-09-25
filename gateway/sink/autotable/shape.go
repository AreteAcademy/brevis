// Package autotable routes each event to the table its own payload names, and
// creates that table when it is absent.
//
// It writes nothing. `into` names the destination that does, and every table it
// creates has the SAME FOUR COLUMNS -- so creating one is a template rather
// than a decision, and the same package serves Postgres, MySQL, BigQuery and
// Redshift without knowing which it is talking to.
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

// The four columns every auto-created table has, and nothing else.
//
// One JSON column instead of a column per field, which is the decision this
// package is built on. A column per field buys types nobody declared and pays
// for them with schema-change quota, a write-stream reopen per new field, and
// a metadata race between replicas. A JSON column pays none of that:
//
//   - a new field is a new key, so there is no DDL, no quota and no race
//   - the poison batch cannot happen: there is no union of fields to reconcile
//   - it is queryable the day it arrives -- JSON_VALUE(data, '$.surprise')
//
// It is also what the market converged on: Fivetran, Airbyte and Snowpipe all
// land into a raw layer that cannot fail and model it downstream.
const (
	ColumnID         = sdk.ColumnIngestionID
	ColumnIngestedAt = "ingested_at"
	ColumnOccurredAt = "occurred_at"
	ColumnData       = "data"
)

// schemaFor is the table every route creates. One declaration serves every
// destination: the SDK's DDL generator turns TypeJSON into JSON on BigQuery,
// JSONB on Postgres, JSON on MySQL and SUPER on Redshift.
//
// `unique` is the half that has to match the write mode, and getting it wrong
// breaks the table in one direction or the other:
//
//	merge   NEEDS the constraint. Postgres and MySQL refuse DedupMerge without
//	        a unique index on ingestion_id, so a table created without it could
//	        never be merged into -- the create succeeds and every load refuses.
//	append  must NOT have it. Every delivery is meant to land, and a unique
//	        constraint would reject the second one as a duplicate, which is the
//	        opposite of what `append` promises.
//
// BigQuery has no unique constraints and its MERGE needs none, so the flag is
// left off there: declaring it would be refused by the dialect, correctly.
func schemaFor(unique bool) sdk.Schema {
	return sdk.Schema{
		{Name: ColumnID, Type: sdk.TypeString, Required: true, Unique: unique},
		{Name: ColumnIngestedAt, Type: sdk.TypeTimestamp, Required: true},
		{Name: ColumnOccurredAt, Type: sdk.TypeTimestamp},
		{Name: ColumnData, Type: sdk.TypeJSON},
	}
}

// PartitionBy is the column a created table is partitioned on, by day.
//
// `ingested_at` and NOT `occurred_at`: a producer's clock can be wrong or
// absent, and a partition column a client controls is a client that can write
// into 2035. An unpartitioned landing table is a query bill that grows forever,
// so this is not optional.
const PartitionBy = ColumnIngestedAt

// ClusterBy is the column a created table is clustered on: the merge key, which
// is what a lookup by identity uses.
var ClusterBy = []string{ColumnID}

// shape turns a producer's event into the four columns.
//
// Everything the producer sent goes into `data`, untouched, INCLUDING the field
// that named the table: a consumer reading the row should see what was posted,
// not what the gateway thought was interesting.
func shape(e map[string]any, now time.Time) (map[string]any, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("the event is not JSON: %w", err)
	}
	row := map[string]any{
		ColumnIngestedAt: now.UTC().Format(time.RFC3339),
		ColumnData:       string(body),
	}
	// occurred_at travels only when the producer sent one. Absent is NULL and
	// not now(): a made-up event time is worse than a missing one, because
	// nothing downstream can tell it apart from a real one.
	if at := gateway.Text(e[ColumnOccurredAt]); at != "" {
		row[ColumnOccurredAt] = at
	}
	return row, nil
}

// identify computes the row's ingestion_id.
//
// The gateway's best sentence is that a 503 is safe to retry, because the
// ingestion_id is a frozen function of the event. That holds here too, but the
// inputs are different: a declared stream names four fields its owner knows,
// and a producer posting {table_name, data} names none.
//
//	uuid5(ns, table | idempotency_key)      when the producer sent one
//	uuid5(ns, table | sha256(canonical))    otherwise
//
// The producer's key is always better: it means "these two requests are the
// same business fact", which only they know. The fingerprint means "these two
// requests have identical bytes" -- a good approximation and a WORSE PROMISE,
// because two genuinely distinct events with byte-identical data collapse into
// one. Say that to producers rather than letting them find it.
func identify(table string, e map[string]any) (string, error) {
	key := gateway.Text(e[KeyField])
	if key == "" {
		sum, err := fingerprint(e)
		if err != nil {
			return "", err
		}
		key = "sha256:" + sum
	}
	env := sdk.Envelope{
		Provider:  Provider,
		Entity:    table,
		SourceKey: key,
		RecordTS:  gateway.Text(e[ColumnOccurredAt]),
	}
	return env.IngestionID()
}

// KeyField is the field a producer uses to say "this is the same fact as
// before". Optional, and naming it is the difference between a promise and an
// approximation.
const KeyField = "idempotency_key"

// Provider is the constant this package stamps into every id, so an id minted
// here can never collide with one a declared stream minted for the same table
// name and key.
const Provider = "auto_table"

// fingerprint is a sha256 over the event in CANONICAL form: keys sorted at
// every level, no insignificant whitespace.
//
// Canonical because Go's map iteration is randomised, so a plain json.Marshal
// of a map produces a different byte string on every call -- and an id built on
// that would differ between two deliveries of the same event, which is the one
// thing it exists to prevent. encoding/json happens to sort a map's keys today;
// this does not rely on that, because "happens to" is how a frozen id thaws.
func fingerprint(e map[string]any) (string, error) {
	var b strings.Builder
	if err := canonical(&b, e); err != nil {
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
