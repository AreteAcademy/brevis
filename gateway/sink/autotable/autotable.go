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
	// BigQuery has no unique constraints and its MERGE needs none; Postgres
	// and MySQL refuse DedupMerge without one. So the constraint follows the
	// write mode AND the destination, and neither alone is enough.
	unique := s.Into.Write == gateway.WriteMerge && s.Into.Type != gateway.SinkBigQuery

	sh, err := shaperFor(s.Shape)
	if err != nil {
		return nil, err
	}

	r := &router{build: b, names: n, unique: unique, shape: sh,
		stream: b.Stream, gateway: b.Gateway,
		meta: newMetastore(s.Metastore.TTL), made: map[string]gateway.Sinker{}, now: time.Now}
	if _, err := r.sinkFor(b.Ctx, probeTable, nil); err != nil {
		return nil, err
	}
	r.forget(probeTable, nil)
	return r, nil
}

// probeTable is the name the startup check builds against. It is never written
// to: the sink built for it is discarded before the first request.
const probeTable = "brevis_probe"

func (r *router) forget(table string, record sdk.Schema) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.made, table+"\x00"+fingerprintOf(record))
	r.names.refund()
}

// router groups a batch by the table each event names, and writes each group
// through a sink of its own.
//
// One Sinker that fans out INTERNALLY rather than a change to the pipe, which
// is what keeps everything around it working unchanged: the retry, the dead
// letter, the drain and the metrics all still see one destination per stream.
type router struct {
	build gateway.Build
	names *names
	meta  *metastore

	// unique says whether the created table carries a UNIQUE constraint on
	// ingestion_id: required by `merge` and wrong for `append`.
	unique bool

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
	return r.names.check(env.table)
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
			return wrote, fmt.Errorf("table %s: %w", table, err)
		}
		r.meta.put(table, true, r.now())
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
	if exists, known := r.meta.get(table, now); !known || !exists {
		if err := r.names.admit(table, now); err != nil {
			return nil, err
		}
	}

	into := *r.build.Sink.Into
	into.Table = table

	// The four columns, the partition and the cluster travel with the
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
		Target: &gateway.Target{
			Schema:   append(fixed(r.unique), record...),
			DedupKey: ColumnID,
			// Additive, and only additive. A field the record grew becomes a
			// column; nothing is ever dropped or narrowed.
			//
			// A batch that loses the race to ALTER fails, and the pipe's
			// ordinary retry is what resolves it: four attempts over roughly
			// seven seconds, and by the second the winner's column is in the
			// catalogue. That is the buffer -- there is no second one, because
			// a batch waiting for DDL and a batch waiting for a worker are the
			// same thing.
			//
			// What is NOT solved here is the metadata quota with many
			// replicas: ten detecting one new field is ten ALTERs, and
			// BigQuery allows five per table per ten seconds. That needs a
			// shared debounce, which needs a shared metastore.
			Evolve:      sdk.EvolveAdditive,
			PartitionBy: PartitionBy,
			ClusterBy:   ClusterBy,
			Create:      true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("table %s: %w", table, err)
	}
	r.made[key] = s
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
