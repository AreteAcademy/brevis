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
	if strings.TrimSpace(s.TableFrom) == "" {
		return nil, fmt.Errorf("`table_from` is empty: name the field of the event " +
			"that says which table it belongs in")
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
			"from each event's %q instead. Remove it", s.Into.Table, s.TableFrom)
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

	r := &router{build: b, field: s.TableFrom, names: n, unique: unique,
		meta: newMetastore(0), made: map[string]gateway.Sinker{}, now: time.Now}
	if _, err := r.sinkFor(b.Ctx, probeTable); err != nil {
		return nil, err
	}
	r.forget(probeTable)
	return r, nil
}

// probeTable is the name the startup check builds against. It is never written
// to: the sink built for it is discarded before the first request.
const probeTable = "brevis_probe"

func (r *router) forget(table string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.made, table)
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
	field string
	names *names
	meta  *metastore

	// unique says whether the created table carries a UNIQUE constraint on
	// ingestion_id: required by `merge` and wrong for `append`.
	unique bool

	// now is a field so a test can control the hour the rate limit counts in.
	now func() time.Time

	mu   sync.Mutex
	made map[string]gateway.Sinker
}

func (r *router) Describe() string {
	return fmt.Sprintf("auto_table:%s→%s", r.field, r.build.Sink.Into.Type)
}

// Admit refuses one event before it is buffered, which is what keeps a bad
// table name from being a poison batch.
//
// Refusing it at WRITE time would fail the whole batch: one malformed event
// from one producer would bury the events of every other producer in the same
// flush window, and none of them would be told. Here the producer gets the
// reason in the response and everybody else's events land.
func (r *router) Admit(e map[string]any) error {
	table := gateway.Text(e[r.field])
	if table == "" {
		return fmt.Errorf("no %q, and that field is what says which table this "+
			"belongs in", r.field)
	}
	return r.names.check(table)
}

// Write groups the batch and writes each table's rows.
//
// A group that fails fails the WHOLE batch, deliberately. The pipe retries a
// batch and then buries it whole, and reporting success for a batch where one
// table refused would lose those rows with nothing said. The dead letter
// carries the reason, and the reason names the table.
func (r *router) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	groups, err := r.group(batch)
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
		sink, err := r.sinkFor(ctx, table)
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

// group shapes every event into the four columns and files it under its table.
func (r *router) group(batch []gateway.Envelope) (map[string][]gateway.Envelope, error) {
	now := r.now()
	out := map[string][]gateway.Envelope{}

	for i, env := range batch {
		e, ok := env.Payload.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("event %d is not a JSON object", i)
		}
		table := gateway.Text(e[r.field])
		if table == "" {
			return nil, fmt.Errorf("event %d has no %q, and that field is what says "+
				"where it goes", i, r.field)
		}
		if err := r.names.check(table); err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}

		row, err := shape(e, now)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
		id, err := identify(table, e)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
		row[ColumnID] = id

		out[table] = append(out[table], sdk.Envelope{Payload: row})
	}
	return out, nil
}

// sinkFor returns the destination for one table, building it once.
//
// The rate limit is charged HERE and only when the table is new to this
// process, which is what makes it bound creations rather than writes: a table
// that already exists is never slowed by one that does not.
func (r *router) sinkFor(ctx context.Context, table string) (gateway.Sinker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.made[table]; ok {
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
		Ctx:    ctx,
		Sink:   into,
		Stores: r.build.Stores,
		Sinks:  r.build.Sinks,
		Target: &gateway.Target{
			Schema:      schemaFor(r.unique),
			PartitionBy: PartitionBy,
			ClusterBy:   ClusterBy,
			Create:      true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("table %s: %w", table, err)
	}
	r.made[table] = s
	return s, nil
}
