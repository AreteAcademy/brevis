package autotable

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// New builds the router.
//
// It resolves `into` through the SAME registry the outer sink came from, so a
// binary that did not compile in the BigQuery driver cannot route into it --
// and says so by name, at startup, rather than on the first table.
func New(b gateway.Build) (gateway.Sinker, error) {
	s := b.Sink
	if strings.TrimSpace(s.TableFrom) != "" {
		// v1 named the field; v2 does not, because the envelope is fixed.
		return nil, fmt.Errorf("`table_from` is no longer a setting: the envelope "+
			"names the table in %q, always. Remove it", FieldTable)
	}
	if s.Into == nil {
		return nil, fmt.Errorf("`into` is empty: auto_table routes and does not " +
			"write, so it needs a destination that does")
	}
	if s.Into.Type == Sink {
		return nil, fmt.Errorf("`into` is another auto_table, which routes into " +
			"nothing")
	}
	if strings.TrimSpace(s.Into.Table) != "" {
		// A fixed table under a router is a contradiction that would silently
		// win: every event would land in it, and `table_from` would do nothing.
		return nil, fmt.Errorf("`into.table` is %q, and auto_table takes the table "+
			"from each event's %q instead. Remove it", s.Into.Table, FieldTable)
	}

	n, err := newNames(s.Naming.Pattern, s.Naming.Allow, s.Naming.MaxNewPerHour)
	if err != nil {
		return nil, err
	}

	// Build the destination once, here, and throw it away.
	//
	// The real ones are built lazily, one per table, which would mean a
	// missing connection string or a driver that cannot create a table is
	// discovered on the first event instead of at startup -- and this
	// package's whole job is turning a payload into DDL, which is the last
	// place to find out late. The probe name never reaches a database: nothing
	// connects until a Write.

	// Two things follow the write mode, and they are not the same thing.
	//
	// `merging` is the mode itself, and it decides whether an event must name
	// a record: `merge` holds one row per record, so a row that names none is
	// a row the resolving query cannot partition by.
	//
	// `unique` is the UNIQUE constraint on the id, and it needs the
	// DESTINATION too: BigQuery has no unique constraints and its MERGE needs
	// none, while Postgres and MySQL refuse DedupMerge without one.
	merging := s.Into.Write == gateway.WriteMerge
	unique := merging && s.Into.Type != gateway.SinkBigQuery

	sh, err := shaperFor(s.Shape)
	if err != nil {
		return nil, err
	}
	if b.Meta == nil {
		return nil, fmt.Errorf("auto_table has no metastore, and it needs one even " +
			"to talk to itself: `memory` is the default and always available")
	}
	ttl := s.Metastore.TTL
	if ttl <= 0 {
		ttl = gateway.DefaultMetastoreTTL
	}

	r := &router{build: b, names: n, unique: unique, merging: merging, shape: sh,
		stream: b.Stream, gateway: b.Gateway,
		meta: &coordinator{store: b.Meta, stream: b.Stream, ttl: ttl},
		made: map[string]gateway.Sinker{}, now: time.Now}
	if _, err := r.open(b.Ctx, probeTable, nil); err != nil {
		return nil, err
	}
	return r, nil
}

// probeTable is the name the startup check builds against. It is never
// written to, never claimed and never counted: it is a validation, and the
// sink built for it is thrown away.
const probeTable = "brevis_probe"

// router groups a batch by the table each event names, and writes each group
// through a sink of its own.
//
// One Sinker that fans out INTERNALLY rather than a change to the pipe, which
// is what keeps everything around it working unchanged: the retry, the dead
// letter, the drain and the metrics all still see one destination per stream.
type router struct {
	build gateway.Build
	names *names
	meta  *coordinator

	// unique says whether the created table carries a UNIQUE constraint on
	// ingestion_id: required by `merge` and wrong for `append`.
	unique bool

	// merging is `write: merge`, and it is what makes `unique_key` mandatory.
	// See DefaultUniqueKey for why the two modes answer differently.
	merging bool

	// shape decides what the record contributes to the row: one JSON column,
	// or a column per field.
	shape shaper

	// stream and gateway are stamped onto every row, so a table fed by several
	// routes still says which one wrote each line.
	stream, gateway string

	// now is a field so a test can control the hour the rate limit counts in.
	now func() time.Time

	mu   sync.Mutex
	made map[string]gateway.Sinker
}

func (r *router) Describe() string {
	return fmt.Sprintf("auto_table[%s]→%s", r.shape.name(), r.build.Sink.Into.Type)
}

// Admit refuses one event before it is buffered, which is what keeps a bad
// table name from being a poison batch.
//
// Refusing it at WRITE time would fail the whole batch: one malformed event
// from one producer would bury the events of every other producer in the same
// flush window, and none of them would be told. Here the producer gets the
// reason in the response and everybody else's events land.
func (r *router) Admit(e map[string]any) error {
	env, err := open(e)
	if err != nil {
		return err
	}
	if err := r.names.check(env.table); err != nil {
		return err
	}
	if err := r.needsKey(env); err != nil {
		return err
	}
	// The record has to be shapeable too, and this line is the one that was
	// missing. `open` and `names` cover the envelope and the table name; the
	// FIELD names were only ever seen at write time, on a whole batch, so one
	// `my-field` buried every other producer's events in the same flush window
	// -- with every one of them already answered 202.
	return r.shape.validate(env.record)
}

// Measure stamps how large this event arrived and says which table its volume
// belongs to.
//
// It OVERWRITES rather than filling a gap: the field name is the gateway's own,
// and a producer who sends it must not be able to report their own volume. It
// is the same rule the `brevis_` prefix enforces inside `data`, applied to the
// one control field the pipe writes from outside.
//
// The returned table is what the volume metrics are labelled by -- see
// gateway.Measurer for why the name travels back instead of the metrics
// travelling in. An envelope this cannot read returns empty, and that event is
// simply not attributed: refusing here would be a refusal at the wrong seam,
// and Admit is about to give the producer the real reason.
func (r *router) Measure(e map[string]any, bytes int) string {
	e[ColumnReceivedBytes] = int64(bytes)

	table := gateway.Text(e[FieldTable])
	if r.names.check(table) != nil {
		return ""
	}
	return table
}

// needsKey is the write mode's half of the unique-key rule.
//
// It lives on the router and not in `open` because `open` parses an envelope
// and knows nothing about where it is going -- and the answer is entirely
// about where it is going. See DefaultUniqueKey for both halves.
func (r *router) needsKey(env envelope) error {
	if env.key != "" || !r.merging {
		return nil
	}
	return fmt.Errorf("%q.%q is missing or empty, and this stream writes with "+
		"`merge`, which keeps one row per record -- so every event has to say "+
		"which record it is about. Send %q, name another field in %q, or use "+
		"`write: append`, where a row need not be about a record at all",
		FieldData, DefaultUniqueKey, DefaultUniqueKey, FieldUniqueKey)
}

// Write groups the batch and writes each table's rows.
//
// A group that fails fails the WHOLE batch, deliberately. The pipe retries a
// batch and then buries it whole, and reporting success for a batch where one
// table refused would lose those rows with nothing said. The dead letter
// carries the reason, and the reason names the table.
func (r *router) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	groups, schemas, err := r.group(batch)
	if err != nil {
		return 0, err
	}

	// Sorted, so a batch produces the same sequence of writes every time. Two
	// runs that differ only in map order are two runs nobody can compare.
	tables := make([]string, 0, len(groups))
	for t := range groups {
		tables = append(tables, t)
	}
	sort.Strings(tables)

	var wrote int64
	for _, table := range tables {
		sink, err := r.sinkFor(ctx, table, schemas[table])
		if err != nil {
			return wrote, err
		}
		n, err := sink.Write(ctx, groups[table])
		wrote += n
		if err != nil {
			// The destination disagreed with what we believed, so what we
			// believed goes. The cache is an optimisation and never the
			// authority: the next batch re-reads the catalogue rather than
			// trusting an entry the server just contradicted.
			r.invalidate(ctx, table)
			return wrote, fmt.Errorf("table %s: %w", table, err)
		}
		r.meta.learn(ctx, table, true)
	}
	return wrote, nil
}

// group opens each envelope and files the row under its table.
//
// Every refusal here is per EVENT and reaches the producer in the response --
// the batch is not failed for one malformed member, which is the poison batch
// this package had once and does not have again.
func (r *router) group(batch []gateway.Envelope) (map[string][]gateway.Envelope, map[string]sdk.Schema, error) {
	now := r.now()
	rows := map[string][]gateway.Envelope{}
	schemas := map[string]sdk.Schema{}

	for i, e := range batch {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("event %d is not a JSON object", i)
		}
		env, err := open(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}
		if err := r.names.check(env.table); err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}
		if err := r.needsKey(env); err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}

		row, err := env.row(now, r.stream, r.gateway)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}
		shaped, err := r.shape.columns(env.record)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}
		for k, v := range shaped {
			row[k] = v
		}

		declared, err := r.shape.schema(env.record)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}
		schemas[env.table] = merge(schemas[env.table], declared)

		// Provider and Entity travel so the BigQuery driver can write them
		// into the TABLE's description when it creates one. Nothing here reads
		// them back; it is the only answer this design has to "what writes
		// here?", six months later.
		rows[env.table] = append(rows[env.table], sdk.Envelope{
			Provider: Provider,
			Entity:   env.table,
			Payload:  row,
		})
	}
	return rows, schemas, nil
}

// merge is the union of two declarations, by name.
//
// A batch holds N records for one table and they need not carry the same
// fields. The union is what the table has to have; a record missing one of them
// writes NULL there, which is what a landing table legitimately does.
func merge(into, add sdk.Schema) sdk.Schema {
	seen := make(map[string]bool, len(into))
	for _, c := range into {
		seen[c.Name] = true
	}
	for _, c := range add {
		if !seen[c.Name] {
			into = append(into, c)
			seen[c.Name] = true
		}
	}
	return into
}

// sinkFor returns the destination for one table, building it once.
//
// The rate limit is charged HERE and only when the table is new to this
// process, which is what makes it bound creations rather than writes: a table
// that already exists is never slowed by one that does not.
func (r *router) sinkFor(ctx context.Context, table string, record sdk.Schema) (gateway.Sinker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Keyed by the table AND the columns, not by the table alone.
	//
	// A sink holds the declaration it was built with. Caching by table would
	// freeze the FIRST batch's shape: the batch that later carries a new field
	// would hand the driver the old declaration, no change would be planned,
	// and Reconcile would refuse the extra field. The table would be stuck at
	// whatever arrived first.
	key := table + "\x00" + fingerprintOf(record)
	if s, ok := r.made[key]; ok {
		return s, nil
	}

	now := r.now()

	// The rate limit is charged only when the table is new to US, which is
	// what makes it bound creations rather than writes: a table that already
	// exists is never slowed by one that does not.
	if exists, known := r.meta.knows(ctx, table); !known || !exists {
		if err := r.meta.admit(ctx, r.names.max, now); err != nil {
			return nil, err
		}
	}

	// A shape this process has not built is a shape that MIGHT need DDL: the
	// table may be absent, or present without one of these columns. Only the
	// replica that takes the claim finds out -- the others come back later,
	// by which time the column is there and nothing is altered.
	//
	// With `memory` every replica takes its own claim and this debounces
	// nothing, which is exactly what redis and memcached are for.
	if !r.meta.claimDDL(ctx, table, fingerprintOf(record)) {
		return nil, ErrClaimed
	}

	s, err := r.open(ctx, table, record)
	if err != nil {
		return nil, err
	}
	r.made[key] = s
	r.meta.learn(ctx, table, true)
	return s, nil
}

// invalidate drops this table from both caches, so the next batch starts over.
func (r *router) invalidate(ctx context.Context, table string) {
	r.mu.Lock()
	for key := range r.made {
		if strings.HasPrefix(key, table+"\x00") {
			delete(r.made, key)
		}
	}
	r.mu.Unlock()
	r.meta.forget(ctx, table)
}

// open builds the destination for one table, and coordinates nothing.
//
// Separate from sinkFor because the STARTUP PROBE needs it. Going through
// sinkFor meant the probe took a claim, and the second replica to start lost
// it and REFUSED TO START -- "another replica is altering this table for this
// shape". Losing a debounce is a batch to retry; it can never be a reason a
// gateway does not come up.
//
// It also kept the probe out of the creation counter, which it had no business
// spending.
func (r *router) open(ctx context.Context, table string, record sdk.Schema) (gateway.Sinker, error) {
	into := *r.build.Sink.Into
	into.Table = table

	// The fixed columns, the partition and the cluster travel with the
	// destination rather than being written into the YAML: every table this
	// creates has the same shape, so creating one is a template and not a
	// decision.
	s, err := gateway.BuildSink(gateway.Build{
		Ctx:     ctx,
		Sink:    into,
		Stores:  r.build.Stores,
		Sinks:   r.build.Sinks,
		Stream:  r.stream,
		Gateway: r.gateway,
		Meta:    r.build.Meta,
		Target: &gateway.Target{
			Schema:   append(fixed(r.unique, r.merging), record...),
			DedupKey: ColumnID,
			// Additive, and only additive. A field the record grew becomes a
			// column; nothing is ever dropped or narrowed.
			//
			// A batch that loses the race to ALTER fails, and the pipe's
			// ordinary retry resolves it -- a batch waiting for DDL and a
			// batch waiting for a worker are the same thing, so there is no
			// second buffer.
			//
			// READ BY POSTGRES AND MYSQL ONLY. The BigQuery sink never looks
			// at this field, and nothing in sdk/load patches an existing
			// table's schema -- the single table.Update there sets a
			// description and labels. So with `into: bigquery` a new field
			// makes the LOAD fail, the batch is retried and then buried.
			//
			// TestIntegrationBigQueryDoesNotEvolveASchemaYet pins that, and
			// fails the day it stops being true.
			Evolve:      sdk.EvolveAdditive,
			PartitionBy: PartitionBy,
			ClusterBy:   ClusterBy,
			Create:      true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("table %s: %w", table, err)
	}
	return s, nil
}

// fingerprintOf names a column set, so a sink is reused while the shape holds
// and rebuilt when it moves. The declaration is already sorted, so this is
// stable without sorting again.
func fingerprintOf(record sdk.Schema) string {
	var b strings.Builder
	for _, c := range record {
		b.WriteString(c.Name)
		b.WriteByte(' ')
		b.WriteString(string(c.Type))
		b.WriteByte(',')
	}
	return b.String()
}
