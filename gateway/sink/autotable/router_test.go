package autotable

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// The wires from the config to the router, which compile whether or not they
// are connected.
//
// Both of these were written, both were right, and a mutation that cut them
// left every test passing: the package tests exercise newMetastore and shape
// directly, and the integration test reads rows out of Postgres, which has no
// table description to check.

// The TTL a file names is the TTL the cache uses.
//
// Cut, it silently falls back to the default -- which is the same number, so
// nothing anywhere would notice until somebody set a different one and it did
// nothing.
func TestTheConfiguredTTLReachesTheCache(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:      gateway.SinkAutoTable,
		Metastore: gateway.MetastoreConfig{Type: gateway.MetastoreMemory, TTL: minutes(5)},
		Into:      &gateway.Sink{Type: "probe"},
	})
	if r.meta.ttl != 5*time.Minute {
		t.Errorf("the cache's TTL is %s, and the file said 5m", r.meta.ttl)
	}
}

// Provider and Entity travel on the envelope, because the BigQuery driver
// writes them into the TABLE's description when it creates one:
//
//	Written by auto_table/app_orders via the Brevis SDK since 2026-09-25.
//
// Nothing here reads them back -- the id is already computed -- so cutting
// them breaks only a string on a table nobody queries in a test. It is also
// the only answer this design has to "what writes here?", six months later.
func TestTheEnvelopeCarriesWhatDescribesTheTable(t *testing.T) {
	r := build(t, gateway.Sink{
		Type: gateway.SinkAutoTable,
		Into: &gateway.Sink{Type: "probe"},
	})

	groups, _, err := r.group([]gateway.Envelope{{Payload: map[string]any{
		"table_name": "app_orders", "data": map[string]any{"id": "A-3"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rows := groups["app_orders"]
	if len(rows) != 1 {
		t.Fatalf("the batch grouped into %d rows", len(rows))
	}
	if rows[0].Provider != Provider {
		t.Errorf("Provider is %q, want %q", rows[0].Provider, Provider)
	}
	// The TABLE and not a constant: the description names which table it is,
	// and the same string on every table would say nothing.
	if rows[0].Entity != "app_orders" {
		t.Errorf("Entity is %q, want the table name", rows[0].Entity)
	}
}

// build makes a router against a sink that writes nowhere, so these can run
// with no database at all.
func build(t *testing.T, s gateway.Sink) *router {
	t.Helper()
	return buildWith(t, s, gateway.NewMemoryMetastore())
}

func buildWith(t *testing.T, s gateway.Sink, meta gateway.Metastore) *router {
	t.Helper()
	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	sinks.MustRegister("probe", func(gateway.Build) (gateway.Sinker, error) {
		return nowhere{}, nil
	})

	built, err := gateway.BuildSink(gateway.Build{
		Ctx: context.Background(), Sink: s, Sinks: sinks,
		Meta: meta,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := built.(*router)
	if !ok {
		t.Fatalf("built a %T", built)
	}
	return r
}

type nowhere struct{}

func (nowhere) Describe() string { return "nowhere" }
func (nowhere) Write(context.Context, []sdk.Envelope) (int64, error) {
	return 0, nil
}

// A field name that cannot be a column has to be refused by Admit, not by
// Write.
//
// This was a real poison batch and it is worth writing down how it looked. Four
// events in one request, one of them carrying `meu-campo`:
//
//	{"accepted":4,"rejected":null}   ← all four told 202
//
// then, two seconds later, the whole batch in the dead letter with a reason
// naming event 2. The three well-formed events belonged to other producers,
// none of whom did anything wrong and none of whom were told. At the batch
// sizes the load test produced -- roughly 8,700 events -- one bad field name
// buries eight thousand.
//
// `open` and `names.check` were already behind Admit. The FIELD names were not,
// because they are the shape's business and the shape only ran at write time.
func TestAdmitRefusesAFieldThatCannotBeAColumn(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:  gateway.SinkAutoTable,
		Shape: ShapeColumns,
		Into:  &gateway.Sink{Type: "probe"},
	})

	err := r.Admit(map[string]any{
		FieldTable: "app_orders",
		FieldData:  map[string]any{"id": "A-1", "meu-campo": "x"},
	})
	if err == nil {
		t.Fatal("Admit took a record whose field cannot be a column; at write " +
			"time that fails the whole batch, and every event in it was already " +
			"answered 202")
	}

	// And the same record is fine under `document`, where the record becomes
	// one JSON column and a key is just a key. Refusing it there would be a
	// rule borrowed from the other shape.
	d := build(t, gateway.Sink{
		Type:  gateway.SinkAutoTable,
		Shape: ShapeDocument,
		Into:  &gateway.Sink{Type: "probe"},
	})
	if err := d.Admit(map[string]any{
		FieldTable: "app_orders",
		FieldData:  map[string]any{"id": "A-1", "meu-campo": "x"},
	}); err != nil {
		t.Errorf("document refused %q, and under that shape it is a key in a "+
			"JSON document and never a column name: %v", "meu-campo", err)
	}
}

// Everything Write can refuse about a record, Admit has to refuse first.
//
// The property, rather than one example: if `group` would fail on a record,
// the producer must learn it in the response instead of the batch learning it
// in the dead letter.
func TestAdmitCatchesWhatWriteWouldHaveRefused(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:  gateway.SinkAutoTable,
		Shape: ShapeColumns,
		Into:  &gateway.Sink{Type: "probe"},
	})

	for _, record := range []map[string]any{
		{"id": "A-1", "meu-campo": "x"}, // a hyphen
		{"id": "A-1", "2fast": "x"},     // starts with a digit
		{"id": "A-1", "com espaço": 1},  // a space
		{"id": "A-1", "": 1},            // empty
	} {
		e := map[string]any{FieldTable: "app_orders", FieldData: record}

		admitted := r.Admit(e) == nil
		_, _, err := r.group([]gateway.Envelope{{Payload: e}})
		written := err == nil

		if admitted != written {
			t.Errorf("record %v: Admit said %v and the write path said %v. A "+
				"record the write path refuses has to be refused per event, or "+
				"it takes the batch with it", record, admitted, written)
		}
	}
}

// `unique_key` is required by `merge` and optional under `append`.
//
// The two modes mean different things and the rule follows the meaning rather
// than being one rule for both. `merge` keeps one row per record, and the
// query that resolves the current version partitions by
// `brevis_record_key` -- so a merge table full of rows naming no record is a
// table nobody can resolve. `append` is a log, and a log entry need not be
// about a record at all: an audit line, a webhook, a metric sample.
//
// Demanding an `id` there would make producers invent one, which is worse than
// NULL because it looks real.
func TestTheKeyIsRequiredOnlyByMerge(t *testing.T) {
	keyless := map[string]any{
		FieldTable: "app_orders",
		FieldData:  map[string]any{"total": 150, "uf": "SP"},
	}

	merge := build(t, gateway.Sink{
		Type: gateway.SinkAutoTable,
		Into: &gateway.Sink{Type: "probe", Write: gateway.WriteMerge},
	})
	err := merge.Admit(keyless)
	if err == nil {
		t.Error("merge took an event that names no record; the resolving query " +
			"partitions by brevis_record_key and every one of these rows would " +
			"be invisible to it")
	} else if !strings.Contains(err.Error(), "append") {
		// The refusal has to name the way out, or the producer's only option
		// is to guess -- and they cannot see the YAML.
		t.Errorf("the refusal does not mention `append`: %v", err)
	}

	appendOnly := build(t, gateway.Sink{
		Type: gateway.SinkAutoTable,
		Into: &gateway.Sink{Type: "probe", Write: gateway.WriteAppend},
	})
	if err := appendOnly.Admit(keyless); err != nil {
		t.Errorf("append refused an event that names no record: %v", err)
	}
}

// Under `append` the column has to be nullable, or the row is refused by the
// DATABASE -- at write time, failing the batch around it.
//
// Which is the same failure the field-name check was added against, arriving
// through a different door: the event is admitted, the DDL says NOT NULL, and
// the insert dies with every other producer's events in the batch.
func TestTheRecordKeyColumnIsNullableUnderAppend(t *testing.T) {
	for _, c := range []struct {
		write    string
		required bool
	}{
		{gateway.WriteMerge, true},
		{gateway.WriteAppend, false},
	} {
		r := build(t, gateway.Sink{
			Type: gateway.SinkAutoTable,
			Into: &gateway.Sink{Type: "probe", Write: c.write},
		})
		for _, col := range fixed(r.unique, r.merging) {
			if col.Name != ColumnRecordKey {
				continue
			}
			if col.Required != c.required {
				t.Errorf("write %s: %s NOT NULL = %v, expected %v",
					c.write, ColumnRecordKey, col.Required, c.required)
			}
		}
	}
}

// A keyless record still gets a stable id, and it is the content's.
//
// `Envelope.IngestionID` refuses an empty SourceKey -- rightly, because an id
// over a blank identity is one every keyless row in the table would share. So
// the fingerprint fills the slot, and the same document twice is the same id.
func TestAKeylessRecordIsIdentifiedByItsContent(t *testing.T) {
	env, err := open(map[string]any{
		FieldTable: "app_logs",
		FieldData:  map[string]any{"level": "warn", "msg": "disk is filling"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env.key != "" {
		t.Fatalf("the record names no key and env.key is %q", env.key)
	}

	first, err := env.identify()
	if err != nil {
		t.Fatalf("a keyless record has no id: %v", err)
	}
	again, err := env.identify()
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Errorf("two calls, two ids: %s and %s", first, again)
	}

	// And a different document is a different row, or an append log would
	// collapse every keyless line into one.
	other, _ := open(map[string]any{
		FieldTable: "app_logs",
		FieldData:  map[string]any{"level": "warn", "msg": "disk is full"},
	})
	if id, _ := other.identify(); id == first {
		t.Error("two different documents share an id")
	}
}

// The row carries no `brevis_record_key` at all when there is none, rather
// than an empty string.
//
// NULL and "" are different facts, and the resolving query's
// `partition by brevis_record_key` would gather every keyless row into one
// partition if they all carried "".
func TestAKeylessRowLeavesTheColumnOut(t *testing.T) {
	env, err := open(map[string]any{
		FieldTable: "app_logs",
		FieldData:  map[string]any{"level": "warn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := env.row(time.Now(), "s", "g")
	if err != nil {
		t.Fatal(err)
	}
	if v, present := row[ColumnRecordKey]; present {
		t.Errorf("%s is in the row as %#v; it should be absent, which is NULL",
			ColumnRecordKey, v)
	}
}

// The arrival size reaches the row, and a producer cannot write it.
//
// Measure OVERWRITES rather than filling a gap. The field name is the
// gateway's own, and the whole value of the column is that summing it is
// trustworthy -- a producer who can set their own number can report whatever
// volume they like, which is exactly the rule the `brevis_` prefix enforces
// inside `data`.
func TestTheArrivalSizeIsOursAndNotTheProducersToSet(t *testing.T) {
	r := build(t, gateway.Sink{
		Type: gateway.SinkAutoTable,
		Into: &gateway.Sink{Type: "probe", Write: gateway.WriteAppend},
	})

	e := map[string]any{
		FieldTable: "app_orders",
		FieldData:  map[string]any{"id": "A-1"},
		// A forged value, sat exactly where the gateway writes its own.
		ColumnReceivedBytes: int64(1),
	}

	if table := r.Measure(e, 742); table != "app_orders" {
		t.Errorf("Measure attributed the volume to %q", table)
	}

	env, err := open(e)
	if err != nil {
		t.Fatal(err)
	}
	row, err := env.row(time.Now(), "s", "g")
	if err != nil {
		t.Fatal(err)
	}
	if row[ColumnReceivedBytes] != int64(742) {
		t.Errorf("%s = %v, and the gateway measured 742: a producer's own number "+
			"survived, so summing this column reports what they claim rather than "+
			"what arrived", ColumnReceivedBytes, row[ColumnReceivedBytes])
	}
}

// A table name the rules refuse is attributed to nothing.
//
// The volume label would otherwise be a series named by a string the producer
// chose and `naming` rejected -- unbounded cardinality through the one door
// that bounds it. Admit refuses the event a moment later with the real reason;
// this seam is not the place for it.
func TestAnUnroutableEventIsNotAttributed(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:   gateway.SinkAutoTable,
		Naming: gateway.Naming{Pattern: `^app_[a-z0-9_]{1,40}$`},
		Into:   &gateway.Sink{Type: "probe", Write: gateway.WriteAppend},
	})
	e := map[string]any{FieldTable: "DROP TABLE x", FieldData: map[string]any{"id": "A"}}
	if table := r.Measure(e, 100); table != "" {
		t.Errorf("volume attributed to %q, which `naming` refuses", table)
	}
}

// A row nothing measured leaves the column out, rather than writing zero.
//
// Zero SUMS. A column whose zeros mean "nobody looked" is a column that lies
// in aggregate, and aggregate is the only way this one is ever read.
func TestAnUnmeasuredRowLeavesTheColumnOut(t *testing.T) {
	env, err := open(map[string]any{
		FieldTable: "app_orders",
		FieldData:  map[string]any{"id": "A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := env.row(time.Now(), "s", "g")
	if err != nil {
		t.Fatal(err)
	}
	if v, present := row[ColumnReceivedBytes]; present {
		t.Errorf("%s is in the row as %#v; unmeasured has to be NULL, because a "+
			"zero here is counted by every sum", ColumnReceivedBytes, v)
	}
}

// The column is on every table, whatever the shape or the write mode.
func TestTheArrivalSizeColumnIsAlwaysThere(t *testing.T) {
	for _, write := range []string{gateway.WriteAppend, gateway.WriteMerge} {
		for _, shape := range []string{ShapeDocument, ShapeColumns} {
			r := build(t, gateway.Sink{
				Type:  gateway.SinkAutoTable,
				Shape: shape,
				Into:  &gateway.Sink{Type: "probe", Write: write},
			})
			var found bool
			for _, c := range fixed(r.unique, r.merging) {
				if c.Name == ColumnReceivedBytes {
					found = true
					if c.Type != sdk.TypeInt64 {
						t.Errorf("%s/%s: %s is %s", write, shape, c.Name, c.Type)
					}
					if c.Required {
						t.Errorf("%s/%s: %s is NOT NULL, so a row nobody measured "+
							"would be refused by the database", write, shape, c.Name)
					}
				}
			}
			if !found {
				t.Errorf("%s/%s has no %s", write, shape, ColumnReceivedBytes)
			}
		}
	}
}

// minutes is a pointer to a duration, because MetastoreConfig.TTL is one: the
// three states are absent, zero (never) and a value.
func minutes(n int) *time.Duration {
	d := time.Duration(n) * time.Minute
	return &d
}

// A table that keeps failing charges the creation budget ONCE, not once per
// retry.
//
// The measured shape of issue #34: a build that fails is never cached, so
// every retry went back through admit, which increments before it checks. Four
// table names reached a count of 31 -- and the budget one table's row error
// had spent then refused three genuinely new tables that had done nothing
// wrong.
//
// Counted at the metastore rather than inferred from a refusal, because the
// DDL debounce answers first on a fast retry: ErrClaimed comes back before
// open() is reached, and the charge had already happened by then. Counting the
// Incr is the property itself.
func TestAFailingTableChargesTheBudgetOnce(t *testing.T) {
	counter := &countingMetastore{Metastore: gateway.NewMemoryMetastore()}
	r := buildWith(t, gateway.Sink{
		Type:   gateway.SinkAutoTable,
		Naming: gateway.Naming{Pattern: `^app_[a-z0-9_]{1,40}$`, MaxNewPerHour: 50},
		Into:   &gateway.Sink{Type: "probe", Write: gateway.WriteAppend},
	}, counter)
	ctx := context.Background()

	// Ten attempts at ONE table, each invalidated as a failing write would --
	// which is what the pipe does, four times per batch, until the dead
	// letter.
	for i := 0; i < 10; i++ {
		_, _ = r.sinkFor(ctx, "app_orders", nil)
		r.invalidate(ctx, "app_orders")
	}

	if got := counter.incrs(); got != 1 {
		t.Errorf("ten attempts at one table spent %d of the creation budget; "+
			"the limit bounds the arrival of NAMES, and one name retrying is "+
			"not that", got)
	}
}

// countingMetastore counts what was charged. Everything else is the real one.
type countingMetastore struct {
	gateway.Metastore
	mu sync.Mutex
	n  int
}

func (c *countingMetastore) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Metastore.Incr(ctx, key, ttl)
}

func (c *countingMetastore) incrs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// And the limit still bites when a producer really does invent names.
//
// The companion to the test above: `charged` must not turn the circuit breaker
// off. Six distinct names against a limit of five is what the field is for.
func TestTheBudgetStillRefusesManyNames(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:   gateway.SinkAutoTable,
		Naming: gateway.Naming{Pattern: `^app_[a-z0-9_]{1,40}$`, MaxNewPerHour: 5},
		Into:   &gateway.Sink{Type: "probe", Write: gateway.WriteAppend},
	})
	ctx := context.Background()

	var refused int
	for i := 0; i < 8; i++ {
		if _, err := r.sinkFor(ctx, fmt.Sprintf("app_t%d", i), nil); err != nil {
			refused++
		}
	}
	if refused == 0 {
		t.Error("eight distinct names against a limit of five and nothing was " +
			"refused: the circuit breaker is off")
	}
}

// A name the budget refused stays refused when it comes back.
//
// The companion to `charged`, and the mutation that found it: marking a table
// as charged on a REFUSAL would let the next attempt skip admit entirely and
// walk straight past the limit. The circuit breaker would hold for exactly one
// attempt per name, which is the same as not holding.
func TestARefusedNameIsStillRefusedOnRetry(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:   gateway.SinkAutoTable,
		Naming: gateway.Naming{Pattern: `^app_[a-z0-9_]{1,40}$`, MaxNewPerHour: 2},
		Into:   &gateway.Sink{Type: "probe", Write: gateway.WriteAppend},
	})
	ctx := context.Background()

	// Spend the budget on two names that are allowed.
	for _, name := range []string{"app_a", "app_b"} {
		if _, err := r.sinkFor(ctx, name, nil); err != nil {
			t.Fatalf("%s should have fitted: %v", name, err)
		}
	}

	// The third is refused, and it has to STAY refused however many times the
	// pipe brings it back.
	for i := 0; i < 4; i++ {
		if _, err := r.sinkFor(ctx, "app_c", nil); err == nil {
			t.Fatalf("attempt %d at app_c was allowed after the budget refused "+
				"it; a limit that holds for one attempt per name is not a limit", i+1)
		}
		r.invalidate(ctx, "app_c")
	}
}
