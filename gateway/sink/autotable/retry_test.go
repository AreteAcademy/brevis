package autotable

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// counting counts writes per table and fails the first `failFor[table]` of them.
type counting struct {
	mu      sync.Mutex
	writes  map[string]int
	failFor map[string]int
}

type countingSink struct {
	table string
	log   *counting
}

func (c countingSink) Describe() string { return "counting:" + c.table }

func (c countingSink) Write(context.Context, []sdk.Envelope) (int64, error) {
	c.log.mu.Lock()
	defer c.log.mu.Unlock()
	if c.log.failFor[c.table] > 0 {
		c.log.failFor[c.table]--
		return 0, context.DeadlineExceeded // a failure that does not invalidate the table
	}
	c.log.writes[c.table]++
	return 1, nil
}

func routerCounting(t *testing.T, log *counting) *router {
	t.Helper()
	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	sinks.MustRegister("count", func(b gateway.Build) (gateway.Sinker, error) {
		return countingSink{table: b.Sink.Table, log: log}, nil
	})
	built, err := gateway.BuildSink(gateway.Build{
		Ctx:   context.Background(),
		Sink:  gateway.Sink{Type: gateway.SinkAutoTable, Into: &gateway.Sink{Type: "count"}},
		Sinks: sinks,
		Meta:  gateway.NewMemoryMetastore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return built.(*router)
}

// A retry of a multi-table batch must not rewrite the tables that already
// landed: under `write: append` that is a duplicate row per attempt.
func TestARetryDoesNotRewriteTablesThatLanded(t *testing.T) {
	log := &counting{writes: map[string]int{}, failFor: map[string]int{"b_users": 2}}
	r := routerCounting(t, log)
	batch := batchFor("a_orders", "b_users")

	ctx := gateway.WithBatchKey(context.Background(), "batch-1")
	// The pipe's retry loop: b_users fails twice, then lands.
	var err error
	for attempt := 1; attempt <= 6 && (attempt == 1 || err != nil); attempt++ {
		_, err = r.Write(ctx, batch)
	}
	if err != nil {
		t.Fatalf("the batch never landed: %v", err)
	}
	if got := log.writes["a_orders"]; got != 1 {
		t.Errorf("a_orders was written %d times across the retries, want 1", got)
	}
	if got := log.writes["b_users"]; got != 1 {
		t.Errorf("b_users was written %d times, want 1", got)
	}

	// Another batch is another batch: it writes normally.
	if _, err := r.Write(gateway.WithBatchKey(context.Background(), "batch-2"), batch); err != nil {
		t.Fatal(err)
	}
	if got := log.writes["a_orders"]; got != 2 {
		t.Errorf("a different batch key wrote a_orders, total %d, want 2", got)
	}
}

// The set expires and is bounded.
func TestTheWrittenSetExpiresAndIsBounded(t *testing.T) {
	r := routerCounting(t, &counting{writes: map[string]int{}, failFor: map[string]int{}})
	clock := time.Now()
	r.now = func() time.Time { return clock }

	ctx := gateway.WithBatchKey(context.Background(), "k")
	r.markWritten(ctx, "t")
	if !r.wasWritten(ctx, "t") {
		t.Fatal("a table just written is not remembered")
	}
	clock = clock.Add(landedTTL + time.Second)
	if r.wasWritten(ctx, "t") {
		t.Error("an entry past its TTL still matches")
	}

	for i := 0; i < landedMax*2; i++ {
		r.markWritten(gateway.WithBatchKey(context.Background(), string(rune('a'+i%26))+time.Duration(i).String()), "t")
		if len(r.landed) > landedMax {
			t.Fatalf("the set grew to %d, bound is %d", len(r.landed), landedMax)
		}
	}
	// No key, nothing remembered.
	r.markWritten(context.Background(), "t")
	if r.wasWritten(context.Background(), "t") {
		t.Error("a context without a batch key matched")
	}
}
