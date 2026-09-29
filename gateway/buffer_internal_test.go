package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"gopkg.in/yaml.v3"
)

// Every format reports the bytes it read, because a zero here turns the size
// ceiling into one nothing ever reaches.
//
// Internal, because `decode` is internal and exporting it for a test would be
// adding public API to look at a private decision.
func TestEveryFormatMeasuresWhatItRead(t *testing.T) {
	for _, c := range []struct {
		format, body string
		events       int
	}{
		{FormatJSON, `{"id":"A"}`, 1},
		{FormatArray, `[{"id":"A"},{"id":"B","pad":"aaaaaaaaaaaa"}]`, 2},
		{FormatNDJSON, "{\"id\":\"A\"}\n{\"id\":\"B\",\"pad\":\"aaaaaaaaaaaa\"}\n", 2},
	} {
		t.Run(c.format, func(t *testing.T) {
			got, err := decode(c.format, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != c.events {
				t.Fatalf("%d events, want %d", len(got), c.events)
			}
			for i, a := range got {
				if a.bytes <= 0 {
					t.Errorf("event %d reported %d bytes", i, a.bytes)
				}
				if a.event == nil {
					t.Errorf("event %d decoded to nil", i)
				}
			}
			// Each event's own size, not one number shared out: the second
			// event in both multi-event cases is the longer one.
			if c.events == 2 && got[1].bytes <= got[0].bytes {
				t.Errorf("sizes %d and %d: the longer event did not measure longer",
					got[0].bytes, got[1].bytes)
			}
		})
	}
}

// What comes back when the pool is busy has to be counted again.
//
// `take` zeroes the buffer's byte total, so a batch handed back has to bring
// its sizes with it. Without that the ceiling silently forgets what it is
// still holding -- and it is the one ceiling whose whole job is to stop an
// OOM, so forgetting is the failure rather than an inaccuracy.
func TestBytesSurviveABatchComingBack(t *testing.T) {
	p := &pipe{
		stream: Stream{Buffer: Buffer{
			Flush:      Flush{Records: 1_000_000, Every: time.Hour},
			MaxRecords: 1_000_000,
		}},
		// Unbuffered, and nobody reading: every handoff fails and gives the
		// batch back, which is the path under test.
		queue:   make(chan []sdk.Envelope),
		buckets: map[string]*bucket{},
	}

	if err := p.enqueue(prepared{
		events: []sdk.Envelope{{}, {}}, sizes: []int64{10, 20}, keys: []string{"", ""},
	}); err != nil {
		t.Fatal(err)
	}
	if p.bytes != 30 {
		t.Fatalf("bytes = %d after 10+20", p.bytes)
	}

	p.mu.Lock()
	p.handoff("", TriggerSize)
	p.mu.Unlock()

	if p.bytes != 30 {
		t.Errorf("bytes = %d after the batch came back: the ceiling forgot %d "+
			"bytes it is still holding", p.bytes, 30-p.bytes)
	}
	b := p.buckets[""]
	if b == nil {
		t.Fatal("the bucket did not come back at all")
	}
	if len(b.sizes) != len(b.batch) {
		t.Errorf("%d sizes for %d events: the two have to stay parallel or the "+
			"next handoff hands back the wrong total", len(b.sizes), len(b.batch))
	}
	if p.held != len(b.batch) {
		t.Errorf("held = %d for %d events: the global count and the buckets "+
			"disagree, and the ceiling reads the global one", p.held, len(b.batch))
	}
}

// A flush that the pool refuses is not a flush, and must not be counted as
// one.
//
// Counting it there would report a flush per attempt on a stream whose sink is
// behind -- turning the one metric that says "you are bypassing the BigQuery
// floor" into noise exactly when the stream is in trouble.
func TestARefusedHandoffIsNotCountedAsAFlush(t *testing.T) {
	m := NewMetrics()
	p := &pipe{
		stream:  Stream{Name: "s", Buffer: Buffer{Flush: Flush{Records: 1_000_000, Every: time.Hour}, MaxRecords: 10}},
		queue:   make(chan []sdk.Envelope),
		metrics: m,
		buckets: map[string]*bucket{},
	}
	if err := p.enqueue(prepared{
		events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{""},
	}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.handoff("", TriggerSize)
	p.mu.Unlock()

	var b strings.Builder
	if err := m.Render(&b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), MetricFlushes) {
		t.Errorf("a handoff the pool refused was counted as a flush:\n%s", b.String())
	}
}

// --- the arrival size -------------------------------------------------------

// The volume series are OFF unless the environment says otherwise, and a
// value nobody can parse is off rather than fatal.
//
// Every other instrument here is labelled by what an OPERATOR wrote in a YAML
// file, so the series count is known before the process starts. `table` is
// chosen by the PRODUCER, and nobody should discover a metrics bill because
// they upgraded.
func TestTheVolumeSeriesAreOffUnlessAskedFor(t *testing.T) {
	for _, c := range []struct {
		value string
		on    bool
	}{
		{"", false},
		{"false", false},
		{"0", false},
		{"sim", false}, // unparseable is off, never a failure to start
		{"true", true},
		{"1", true},
		{"TRUE", true},
	} {
		t.Run("["+c.value+"]", func(t *testing.T) {
			t.Setenv(EnvIngestionMetrics, c.value)
			if got := IngestionMetricsEnabled(); got != c.on {
				t.Fatalf("%q read as %v, want %v", c.value, got, c.on)
			}

			m := NewMetrics()
			m.ingested("s", "app_orders", 512)

			var b strings.Builder
			if err := m.Render(&b); err != nil {
				t.Fatal(err)
			}
			has := strings.Contains(b.String(), MetricIngestedBytes)
			if has != c.on {
				t.Errorf("%s present = %v with %s=%q", MetricIngestedBytes, has, EnvIngestionMetrics, c.value)
			}
		})
	}
}

// With the switch on, the two series carry the table and the bytes.
func TestTheVolumeSeriesCarryTheTable(t *testing.T) {
	t.Setenv(EnvIngestionMetrics, "true")
	m := NewMetrics()
	m.ingested("tables", "app_orders", 400)
	m.ingested("tables", "app_orders", 600)
	m.ingested("tables", "app_logs", 100)
	// An event nothing could attribute is not counted anywhere, rather than
	// counted under an empty label -- which would be a series named "".
	m.ingested("tables", "", 999)

	var b strings.Builder
	if err := m.Render(&b); err != nil {
		t.Fatal(err)
	}
	text := b.String()
	for _, want := range []string{
		`brevis_gateway_ingested_bytes_total{stream="tables",table="app_orders"} 1000`,
		`brevis_gateway_ingested_events_total{stream="tables",table="app_orders"} 2`,
		`brevis_gateway_ingested_bytes_total{stream="tables",table="app_logs"} 100`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing:\n  %s\ngot:\n%s", want, text)
		}
	}
	if strings.Contains(text, `table=""`) {
		t.Error("an unattributable event was counted under an empty table label")
	}
}

// --- ttl: 0 means never (issue #35) ----------------------------------------

// The three states of the TTL are three, and a pointer is what keeps them
// apart.
//
// This is the trap the change had to avoid: `ttl:` omitted and `ttl: 0` are
// the same zero in a plain Duration, so making zero mean "never" would have
// flipped every existing config to never-expire in silence. The same reason
// `metrics.addr` is a pointer.
func TestTheTTLTellsSilenceFromZero(t *testing.T) {
	zero := Duration(0)
	five := Duration(5 * time.Minute)

	for _, c := range []struct {
		name string
		in   *Duration
		want time.Duration
	}{
		{"said nothing", nil, DefaultMetastoreTTL},
		{"said zero", &zero, 0},
		{"said a value", &five, 5 * time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := MetastoreConfig{Type: MetastoreMemory, TTL: c.in}
			if err := m.check(); err != nil {
				t.Fatal(err)
			}
			if got := m.CacheTTL(); got != c.want {
				t.Errorf("CacheTTL() = %s, want %s", got, c.want)
			}
		})
	}

	// Negative is still refused: it is neither a duration nor an intention.
	bad := Duration(-time.Second)
	if err := (&MetastoreConfig{Type: MetastoreMemory, TTL: &bad}).check(); err == nil {
		t.Error("a negative ttl was accepted")
	}
}

// The memory backend has to MEAN never, and this is where it did not.
//
// `now.Add(0)` is a moment in the past by the time Get reads it, so a zero TTL
// made every entry expire instantly -- the exact opposite of what `ttl: 0`
// asks for, while Redis read the same zero as "keep forever". Three backends,
// two meanings, and the one nobody had to configure was the broken one.
func TestZeroTTLDoesNotExpireInMemory(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryMetastore()

	if err := m.Put(ctx, "forever", "1", 0); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := m.Get(ctx, "forever"); err != nil || !ok || v != "1" {
		t.Fatalf("a zero-TTL entry read back as (%q, %v, %v); it must not expire",
			v, ok, err)
	}

	// And a positive TTL still expires, or "never" would be the only mode.
	if err := m.Put(ctx, "brief", "1", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok, _ := m.Get(ctx, "brief"); ok {
		t.Error("a 1ms entry was still there after 20ms")
	}
}

// --- process_start_time_seconds (issue #33) --------------------------------

// The start time is on EVERY scrape, including the first one and including a
// scrape of a process that has counted nothing.
//
// The first scrape is the whole point. A collector that does start-time
// adjustment -- the OpenTelemetry Prometheus receiver, which Google Managed
// Prometheus is built on -- anchors a cumulative series at the moment it
// believes it began. Given this gauge it uses the process's start and
// attributes the first scrape's whole value; without it, it infers the start
// from the first scrape and spends that scrape's value as the baseline.
//
// A consumer measured 5,000 events reported as 650, and two flush counters
// that existed and read ZERO -- which a counter does not do. Deltas exact,
// totals wrong.
func TestTheExpositionAlwaysCarriesTheProcessStart(t *testing.T) {
	render := func(m *Metrics) string {
		t.Helper()
		var b strings.Builder
		if err := m.Render(&b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	// A process that has counted nothing still has to say when it started.
	empty := render(NewMetrics())
	if !strings.Contains(empty, "process_start_time_seconds") {
		t.Fatalf("an empty scrape has no start time, so the collector's first "+
			"reading of every series would set its own baseline:\n%s", empty)
	}
	if !strings.Contains(empty, "# TYPE process_start_time_seconds gauge") {
		t.Error("the start time is not declared a gauge")
	}
	// NOT prefixed. Collectors look for exactly this name; `brevis_` in front
	// of it would be correct and useless.
	if strings.Contains(empty, "brevis_gateway_process_start") {
		t.Error("the start time was published under a brevis_ name, which no " +
			"collector looks for")
	}

	// And on a scrape that does carry counters.
	m := NewMetrics()
	m.count(m.received, 7, "s", "json")
	if !strings.Contains(render(m), "process_start_time_seconds") {
		t.Error("the start time is missing once there are counters")
	}
}

// It is the same instant every time, and across a rebuilt Metrics.
//
// A value that moved would tell the collector the series restarted, and it
// would drop everything counted before -- which is the failure this gauge
// exists to fix, arriving through the fix itself.
func TestTheProcessStartDoesNotMove(t *testing.T) {
	read := func() string {
		var b strings.Builder
		if err := NewMetrics().Render(&b); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(b.String(), "\n") {
			if rest, ok := strings.CutPrefix(line, "process_start_time_seconds "); ok {
				return rest
			}
		}
		t.Fatal("no start time in the scrape")
		return ""
	}

	first := read()
	time.Sleep(20 * time.Millisecond)
	if second := read(); second != first {
		t.Errorf("the start time moved between scrapes: %s then %s. A collector "+
			"reads that as the series restarting and drops what came before",
			first, second)
	}

	// And it is a plausible instant: this process started, so it is in the
	// past, and it is not 1970.
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil {
		t.Fatalf("the value is not a float: %q", first)
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if secs > now || now-secs > 3600 {
		t.Errorf("start time %f against now %f: it has to be in the past and "+
			"within this process's lifetime", secs, now)
	}
}

// The value the docs name has to be the value the parser takes.
//
// `ttl: 0` was refused with `cannot unmarshal !!int 0 into time.Duration` --
// a Go type name in front of somebody who wrote a config file, about the one
// value this field's own documentation told them to write. A consumer found it
// by copying it out of the changelog.
//
// Both spellings work now, and a bare number that is NOT zero is refused with
// the unit spelled out: `ttl: 60` would be sixty NANOSECONDS under Go's own
// conversion, which nobody has ever meant.
func TestTheTTLTakesTheSpellingTheDocsUse(t *testing.T) {
	for _, c := range []struct {
		yaml string
		want time.Duration
		bad  string
	}{
		{yaml: "0", want: 0},
		{yaml: "0s", want: 0},
		{yaml: `"0"`, want: 0},
		{yaml: "60s", want: time.Minute},
		{yaml: "500ms", want: 500 * time.Millisecond},
		{yaml: "2h", want: 2 * time.Hour},
		{yaml: "60", bad: "NANOSECONDS"},
		{yaml: "-1", bad: "NANOSECONDS"},
		{yaml: "tarde", bad: "not a duration"},
		{yaml: "true", bad: "a duration is text"},
	} {
		t.Run(c.yaml, func(t *testing.T) {
			var d Duration
			err := yaml.Unmarshal([]byte("ttl: "+c.yaml), &struct {
				TTL *Duration `yaml:"ttl"`
			}{TTL: &d})
			if c.bad != "" {
				if err == nil {
					t.Fatalf("%s was accepted as %s", c.yaml, d)
				}
				if !strings.Contains(err.Error(), c.bad) {
					t.Errorf("the refusal does not say %q: %v", c.bad, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s was refused: %v", c.yaml, err)
			}
			if time.Duration(d) != c.want {
				t.Errorf("%s = %s, want %s", c.yaml, d, c.want)
			}
		})
	}
}

// --- flush.max_age (issue #36, slice 1) ------------------------------------

// The deadline a partial batch waits on.
//
// `every` is the TARGET: flush at this age. `max_age` is the PROMISE: no event
// waits longer than this, whatever defers the target. Until the flush claim
// lands there is nothing to defer, so `every` always wins and this is
// scaffolding -- but the arithmetic is what slice 3 consults, and it is worth
// pinning before anything depends on it.
func TestTheDeadlineIsTheEarlierOfTheTwo(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		name          string
		claim         bool
		every, maxAge time.Duration
		age           time.Duration // how old this bucket already is
		want          time.Duration
	}{
		// Without a claim the target is measured from the bucket's own oldest
		// event, which is what the single batch's timer always meant -- armed
		// once, when the batch went from empty to held.
		{"fresh bucket: the whole target", false, time.Minute, 0, 0, time.Minute},
		{"part way through", false, time.Minute, 0, 20 * time.Second, 40 * time.Second},
		{"target already passed: now", false, time.Minute, 0, 90 * time.Second, 0},
		// Nothing held, so nothing is aging and there is only the target.
		{"nothing held: the target", false, time.Minute, 5 * time.Minute, -1, time.Minute},

		// With a claim the wake is the WINDOW BOUNDARY, the same instant for
		// every replica and every bucket, so a bucket older than the target
		// is NOT due -- it lost a window and waits for the next one. That is
		// the regime `max_age` exists to bound.
		{"claim: the boundary", true, time.Minute, 0, 90 * time.Second, time.Minute},
		// 280s old against a 300s ceiling: 20s left, less than the 60s to the
		// boundary, so the ceiling decides.
		{"claim: the ceiling closes in", true, time.Minute, 5 * time.Minute, 280 * time.Second, 20 * time.Second},
		{"claim: ceiling already passed", true, time.Minute, 5 * time.Minute, 10 * time.Minute, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &pipe{
				stream: Stream{Buffer: Buffer{Flush: Flush{
					Every: c.every, MaxAge: Duration(c.maxAge), Claim: c.claim,
				}}},
				now:     func() time.Time { return base },
				buckets: map[string]*bucket{},
			}
			var b *bucket
			if c.age >= 0 {
				b = &bucket{oldest: base.Add(-c.age)}
			}
			if got := p.deadline(b); got != c.want {
				t.Errorf("deadline = %s, want %s", got, c.want)
			}
		})
	}
}

// The oldest event in the buffer is what the ceiling is measured from, and it
// does not move while the batch fills.
func TestTheBatchRemembersItsOldestEvent(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := base
	p := &pipe{
		stream: Stream{Buffer: Buffer{
			Flush:      Flush{Records: 1_000_000, Every: time.Hour},
			MaxRecords: 1_000_000,
		}},
		queue:   make(chan []sdk.Envelope, 1),
		now:     func() time.Time { return clock },
		buckets: map[string]*bucket{},
	}

	if len(p.buckets) != 0 {
		t.Fatal("an empty buffer has a bucket")
	}
	one := prepared{events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{"a"}}
	if err := p.enqueue(one); err != nil {
		t.Fatal(err)
	}
	if got := p.buckets["a"].oldest; !got.Equal(base) {
		t.Fatalf("oldest = %s, want the moment the first event arrived (%s)", got, base)
	}

	// More events later must not reset the clock: the wait belongs to the
	// event that has waited longest, not to the newest one.
	clock = base.Add(30 * time.Second)
	if err := p.enqueue(one); err != nil {
		t.Fatal(err)
	}
	if got := p.buckets["a"].oldest; !got.Equal(base) {
		t.Errorf("oldest moved to %s when a later event arrived", got)
	}

	// A DIFFERENT key starts its own clock, which is the whole point: a table
	// that arrives late does not inherit the wait of one that arrived early.
	if err := p.enqueue(prepared{
		events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{"b"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.buckets["b"].oldest; !got.Equal(clock) {
		t.Errorf("the second key's oldest = %s, want %s -- it inherited the "+
			"first key's wait", got, clock)
	}

	// And taking a bucket removes it, because what is left of it is nothing.
	p.mu.Lock()
	_ = p.take("a")
	p.mu.Unlock()
	if _, still := p.buckets["a"]; still {
		t.Error("the bucket survived take()")
	}
	if _, gone := p.buckets["b"]; !gone {
		t.Error("taking one bucket took the other")
	}
}

// A ceiling below the target means the target never applies, so the config
// says so instead of behaving that way.
func TestAMaxAgeUnderTheTargetIsRefused(t *testing.T) {
	for _, c := range []struct {
		name          string
		every, maxAge time.Duration
		bad           bool
	}{
		{"no ceiling", time.Minute, 0, false},
		{"equal is fine", time.Minute, time.Minute, false},
		{"above is the point", time.Minute, 5 * time.Minute, false},
		{"below never applies", time.Minute, 30 * time.Second, true},
		{"negative", time.Minute, -time.Second, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &Stream{
				Name: "s", Path: "/x",
				Identity:   Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
				Buffer:     Buffer{Flush: Flush{Every: c.every, Records: 1, MaxAge: Duration(c.maxAge)}},
				Sink:       Sink{Type: "files", Path: "./o/"},
				DeadLetter: Sink{Type: "files", Path: "./d/"},
			}
			err := s.check()
			if c.bad && err == nil {
				t.Fatalf("max_age %s under every %s was accepted", c.maxAge, c.every)
			}
			if !c.bad && err != nil {
				t.Fatalf("refused a valid pair: %v", err)
			}
			if c.bad && c.maxAge > 0 && !strings.Contains(err.Error(), "max_age") {
				t.Errorf("the refusal does not name the field: %v", err)
			}
		})
	}
}

// The drain budget is about what a buffer can be HOLDING, so it reads the
// ceiling where one is declared.
//
// `every` bounds that only while every flush happens on time. From the flush
// claim on, a replica that loses a window keeps filling -- so deriving the
// grace period from the target would print a number too small on the boot
// line, as if it were right.
func TestTheDrainBudgetReadsTheCeiling(t *testing.T) {
	budget := func(every, maxAge time.Duration) time.Duration {
		c := &Config{Streams: []Stream{{
			Buffer: Buffer{Flush: Flush{Every: every, MaxAge: Duration(maxAge)}},
		}}}
		return c.DrainBudget()
	}

	// No ceiling: the target, as before, floored at thirty seconds.
	if got := budget(90*time.Second, 0); got != 90*time.Second {
		t.Errorf("no ceiling: %s, want 90s", got)
	}
	if got := budget(time.Second, 0); got != drainFloor {
		t.Errorf("below the floor: %s, want %s", got, drainFloor)
	}
	// A ceiling above the target is what the buffer can hold.
	if got := budget(60*time.Second, 300*time.Second); got != 300*time.Second {
		t.Errorf("with a ceiling: %s, want 300s -- a replica that loses windows "+
			"holds up to max_age, and the grace period has to cover it", got)
	}
	// Declared drain still wins over both.
	c := &Config{
		Shutdown: Shutdown{Drain: 42 * time.Second},
		Streams:  []Stream{{Buffer: Buffer{Flush: Flush{Every: time.Minute, MaxAge: Duration(time.Hour)}}}},
	}
	if got := c.DrainBudget(); got != 42*time.Second {
		t.Errorf("a declared shutdown.drain was overridden: %s", got)
	}
}

// The wire: arm() uses the deadline, not `every` directly.
//
// The test above exercises the arithmetic and says nothing about whether
// anything consults it — cutting `deadline()` out of `arm()` leaves it green,
// which is the defect this repository keeps finding. This one goes through
// arm() and watches the clock.
//
// It also pins WHEN the deadline is computed: at arm time, not continuously.
// That is why it does not fire today — `arm()` runs when a batch starts, and a
// batch that just started is not old. From the flush claim on, a lost window
// re-arms with an aged batch, and this is the path that closes the ceiling in.
func TestArmUsesTheDeadline(t *testing.T) {
	p := &pipe{
		stream: Stream{Buffer: Buffer{Flush: Flush{
			// An hour away, so only the ceiling can make this fire.
			Every:  time.Hour,
			MaxAge: Duration(60 * time.Millisecond),
		}}},
		queue: make(chan []sdk.Envelope, 1),
		// A pipe production builds always has these, and handoff reaches
		// through the pointer to name the counter before count() can guard
		// it. An incomplete pipe panics rather than skipping the metric.
		metrics: NewMetrics(),
		buckets: map[string]*bucket{},
	}
	p.buckets[""] = &bucket{
		batch: []sdk.Envelope{{}}, sizes: []int64{1}, bytes: 1,
		// Already 50ms old: about 10ms of ceiling left.
		oldest: time.Now().Add(-50 * time.Millisecond),
	}
	p.held, p.bytes = 1, 1

	p.mu.Lock()
	p.arm()
	p.mu.Unlock()

	select {
	case <-p.queue:
		// Flushed on the ceiling, an hour before the target.
	case <-time.After(2 * time.Second):
		t.Fatal("the batch did not flush within two seconds, with a 60ms ceiling " +
			"and an hour-away target: arm() is reading `every` rather than the " +
			"deadline, so max_age can never fire")
	}
}

// --- the metastore is a stream-level promise (issue #36, slice 2) ----------

// `metastore:` is honoured for EVERY stream and was validated for only one
// kind.
//
// `metastoreFor` opens one per stream from `s.Metastore`, whatever the sink —
// and `Sink.check()` validated it behind `if s.Type == SinkAutoTable`. So a
// stream with a direct `bigquery` sink naming redis with no `addr_from` started
// happily and then failed at the first batch, or silently fell back to memory
// and coordinated with nobody. The flush claim makes that field load-bearing
// for exactly those streams.
func TestTheMetastoreIsCheckedForEverySink(t *testing.T) {
	stream := func(sinkType string, m MetastoreConfig) *Stream {
		sink := Sink{Type: sinkType, Path: "./o/", Write: WriteAppend,
			DSNFrom: "D", Project: "p", Dataset: "d", Table: "t", Metastore: m}
		if sinkType == SinkAutoTable {
			sink.Into = &Sink{Type: "postgres", DSNFrom: "D", Write: WriteAppend}
		}
		return &Stream{
			Name: "s", Path: "/x",
			Identity:   Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
			Buffer:     Buffer{Flush: Flush{Every: time.Minute, Records: 1}},
			Sink:       sink,
			DeadLetter: Sink{Type: "files", Path: "./d/"},
		}
	}

	// The bug: redis with no addr_from, on a sink that is not auto_table.
	for _, kind := range []string{"bigquery", "postgres", "files", SinkAutoTable} {
		t.Run(kind+"/redis without addr_from", func(t *testing.T) {
			err := stream(kind, MetastoreConfig{Type: MetastoreRedis}).check()
			if err == nil {
				t.Fatalf("a %s stream naming redis with no addr_from was accepted; "+
					"the flush claim would coordinate with nothing and nobody "+
					"would know why", kind)
			}
			if !strings.Contains(err.Error(), "addr_from") {
				t.Errorf("the refusal does not name the field: %v", err)
			}
		})
	}

	// And a stream that names no metastore still gets memory, unchanged.
	for _, kind := range []string{"bigquery", "postgres", "files"} {
		t.Run(kind+"/silence is memory", func(t *testing.T) {
			s := stream(kind, MetastoreConfig{})
			if err := s.check(); err != nil {
				t.Fatalf("a stream with no metastore was refused: %v", err)
			}
			if s.Sink.Metastore.Type != MetastoreMemory {
				t.Errorf("type = %q, want %q", s.Sink.Metastore.Type, MetastoreMemory)
			}
		})
	}

	// A dead letter is a Sink too, and it has no metastore of its own. The
	// check belongs to the STREAM's sink, so a dead letter naming one must not
	// be silently honoured -- there is no code that would open it.
	t.Run("the dead letter's own metastore is not a thing", func(t *testing.T) {
		s := stream("files", MetastoreConfig{})
		s.DeadLetter.Metastore = MetastoreConfig{Type: MetastoreRedis}
		if err := s.check(); err != nil {
			t.Fatalf("refused: %v", err)
		}
		// Nothing opens it, so nothing validates it. What matters is that it
		// does not become the stream's.
		if s.Sink.Metastore.Type != MetastoreMemory {
			t.Errorf("a dead letter's metastore leaked into the stream's: %q",
				s.Sink.Metastore.Type)
		}
	})
}

// The pipe can reach its stream's metastore, because the flush claim is taken
// on the timer path and the timer lives here.
func TestThePipeHoldsItsMetastore(t *testing.T) {
	m := NewMemoryMetastore()
	p := newPipe(Stream{Name: "s", Buffer: Buffer{Queue: 1, Workers: 0}},
		nil, nil, nil, 1<<20, NewMetrics(), m)
	if p.meta != m {
		t.Error("the pipe does not hold the metastore it was given; the flush " +
			"claim is taken on the timer path and cannot reach it")
	}

	// A pipe built without one is still usable -- nothing coordinates, which
	// is what `memory` does anyway.
	q := newPipe(Stream{Name: "s", Buffer: Buffer{Queue: 1, Workers: 0}},
		nil, nil, nil, 1<<20, NewMetrics(), nil)
	if q.meta != nil {
		t.Error("a pipe given no metastore invented one")
	}
}

// The wire: New() hands the stream's metastore to the pipe.
//
// The test above builds a pipe directly and says nothing about whether
// anything passes one — replacing the lookup with a nil left it green. Third
// time in this file that exercising the function missed the wiring, so this
// one goes through New and reads the pipe it built.
func TestNewGivesThePipeTheStreamsMetastore(t *testing.T) {
	cfg := &Config{
		Name:   "g",
		Listen: Listen{Addr: ":0"},
		Streams: []Stream{{
			Name: "s", Path: "/v1/x",
			Identity: Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
			Buffer:   Buffer{Flush: Flush{Every: time.Minute, Records: 1}},
			Sink: Sink{Type: "fake",
				Metastore: MetastoreConfig{Type: MetastoreMemory}},
			DeadLetter: Sink{Type: "fake"},
		}},
	}
	if err := cfg.check(); err != nil {
		t.Fatal(err)
	}

	// A registered fake, because this is an INTERNAL test: importing
	// gateway/sink/files here would be an import cycle, since that package
	// imports this one. The dead letter goes through the registry too, so
	// WithSink alone would not be enough.
	sinks := NewSinks()
	sinks.MustRegister("fake", func(Build) (Sinker, error) { return nowhereSink{}, nil })

	srv, err := New(cfg, nil, WithSinks(sinks))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close(context.Background()) }()

	if len(srv.pipes) != 1 {
		t.Fatalf("%d pipes", len(srv.pipes))
	}
	if srv.pipes[0].meta == nil {
		t.Fatal("the pipe has no metastore, so the flush claim on the timer " +
			"path would have nothing to ask -- and a stream configured with a " +
			"shared backend would coordinate with nobody")
	}
}

// One metastore per stream, shared by the router and the pipe.
//
// `metastoreFor` caches by stream name, and its comment says why: "opening two
// connections to say the same thing is two things to watch". Asserted here
// rather than through a Server, because reaching the sink's copy from outside
// would need a field on Stream that exists for a test and nothing else.
func TestOneMetastorePerStream(t *testing.T) {
	o := &options{metastores: NewMetastores()}
	cfg := MetastoreConfig{Type: MetastoreMemory}

	first, err := o.metastoreFor(context.Background(), "s", Sink{Metastore: cfg})
	if err != nil {
		t.Fatal(err)
	}
	again, err := o.metastoreFor(context.Background(), "s", Sink{Metastore: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Error("two calls for one stream opened two metastores; the router and " +
			"the pipe would hold different ones")
	}

	other, err := o.metastoreFor(context.Background(), "t", Sink{Metastore: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Error("two streams share one metastore, so their keys would collide")
	}
}

// nowhereSink accepts everything and keeps nothing.
type nowhereSink struct{}

func (nowhereSink) Describe() string { return "nowhere" }
func (nowhereSink) Write(context.Context, []sdk.Envelope) (int64, error) {
	return 0, nil
}

// --- the flush claim (issue #36, slice 3) ----------------------------------

// A claim with no ceiling is unbounded latency, so the config refuses it.
//
// `every` stops being a promise the moment another replica can take the
// window: a loser keeps filling and comes back next window, and with no
// `max_age` there is nothing that says how many times that may happen.
func TestAClaimWithoutACeilingIsRefused(t *testing.T) {
	stream := func(claim bool, maxAge time.Duration) *Stream {
		return &Stream{
			Name: "s", Path: "/x",
			Identity: Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
			Buffer: Buffer{Flush: Flush{
				Every: time.Minute, Records: 1,
				Claim: claim, MaxAge: Duration(maxAge),
			}},
			Sink:       Sink{Type: "files", Path: "./o/"},
			DeadLetter: Sink{Type: "files", Path: "./d/"},
		}
	}

	if err := stream(true, 0).check(); err == nil {
		t.Error("`claim: true` with no max_age was accepted; a replica that keeps " +
			"losing would hold data with nothing saying for how long")
	} else if !strings.Contains(err.Error(), "max_age") {
		t.Errorf("the refusal does not name the field: %v", err)
	}

	if err := stream(true, 5*time.Minute).check(); err != nil {
		t.Errorf("a claim with a ceiling was refused: %v", err)
	}
	if err := stream(false, 0).check(); err != nil {
		t.Errorf("no claim, no ceiling -- today's config -- was refused: %v", err)
	}
}

// Two replicas, one backend, one window: exactly one flushes.
//
// This is the whole point. `flush()` is a per-process AfterFunc, so load jobs
// per table are `replicas × 86400 / every` -- on BigQuery, against 1,500 per
// table per day, two replicas at 60s is 192% of the quota before an HPA does
// anything.
func TestOneWindowIsOneFlushHoweverManyReplicas(t *testing.T) {
	shared := NewMemoryMetastore()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	replica := func() *pipe {
		p := newPipe(Stream{
			Name: "s",
			Buffer: Buffer{Queue: 1, Workers: 0, MaxRecords: 100, Flush: Flush{
				Every: time.Minute, Records: 1_000_000,
				Claim: true, MaxAge: Duration(time.Hour),
			}},
		}, nil, nil, nil, 1<<20, NewMetrics(), shared)
		p.gateway = "g"
		p.now = func() time.Time { return base }
		return p
	}

	var flushed int
	for i, p := range []*pipe{replica(), replica(), replica(), replica()} {
		if err := p.enqueue(prepared{events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{""}}); err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
		// flush() takes the lock itself; holding it here deadlocks.
		p.flush()
		p.mu.Lock()
		held := p.held
		p.mu.Unlock()
		if held == 0 {
			flushed++
		}
	}

	if flushed != 1 {
		t.Errorf("%d of 4 replicas flushed the same window; the budget still "+
			"belongs to the process rather than to the deployment", flushed)
	}
}

// Without a shared backend nothing coordinates, which is exactly what `memory`
// does everywhere else in this design: every replica wins its own claim.
func TestWithoutASharedBackendEveryReplicaFlushes(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	var flushed int
	for range 4 {
		// A metastore EACH, which is what `memory` gives four pods.
		p := newPipe(Stream{
			Name: "s",
			Buffer: Buffer{Queue: 1, Workers: 0, MaxRecords: 100, Flush: Flush{
				Every: time.Minute, Records: 1_000_000,
				Claim: true, MaxAge: Duration(time.Hour),
			}},
		}, nil, nil, nil, 1<<20, NewMetrics(), NewMemoryMetastore())
		p.gateway = "g"
		p.now = func() time.Time { return base }
		if err := p.enqueue(prepared{events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{""}}); err != nil {
			t.Fatal(err)
		}
		p.flush()
		p.mu.Lock()
		held := p.held
		p.mu.Unlock()
		if held == 0 {
			flushed++
		}
	}
	if flushed != 4 {
		t.Errorf("%d of 4 flushed with a metastore each; `memory` coordinates "+
			"nothing and must keep behaving exactly as it did", flushed)
	}
}

// A metastore that is down must not hold data.
//
// The direction claimDDL already chose, and its comment is the argument:
// "refusing here would stop every write on a cache being down, which is the
// one thing a cache must never do". Losing coordination costs load jobs;
// refusing to flush costs the thing the buffer exists to protect.
func TestAnUnreachableBackendFlushesAnyway(t *testing.T) {
	p := newPipe(Stream{
		Name: "s",
		Buffer: Buffer{Queue: 1, Workers: 0, MaxRecords: 100, Flush: Flush{
			Every: time.Minute, Records: 1_000_000,
			Claim: true, MaxAge: Duration(time.Hour),
		}},
	}, nil, nil, nil, 1<<20, NewMetrics(), brokenMetastore{})
	p.gateway = "g"

	if err := p.enqueue(prepared{events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{""}}); err != nil {
		t.Fatal(err)
	}
	p.flush()

	p.mu.Lock()
	held := p.held
	p.mu.Unlock()
	if held != 0 {
		t.Error("the batch was held because the metastore could not be asked; " +
			"a cache being down must never stop a write")
	}
}

// The ceiling is a promise, and a promise the claim can defer is not one.
//
// A replica that has lost every window still leaves at max_age -- it does not
// even ask, because the answer could not change what it must do.
func TestTheCeilingIsNeverClaimed(t *testing.T) {
	// A backend that says no to everything, which is the worst case: some
	// other replica holds every window forever.
	p := newPipe(Stream{
		Name: "s",
		Buffer: Buffer{Queue: 1, Workers: 0, MaxRecords: 100, Flush: Flush{
			Every: time.Minute, Records: 1_000_000,
			Claim: true, MaxAge: Duration(90 * time.Second),
		}},
	}, nil, nil, nil, 1<<20, NewMetrics(), refusingMetastore{})
	p.gateway = "g"

	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := base
	p.now = func() time.Time { return clock }

	if err := p.enqueue(prepared{events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{""}}); err != nil {
		t.Fatal(err)
	}

	// Inside the ceiling: it asks, it loses, it keeps filling.
	clock = base.Add(time.Minute)
	p.flush()
	p.mu.Lock()
	held := p.held
	p.mu.Unlock()
	if held == 0 {
		t.Fatal("it flushed while the ceiling still had room and the claim was refused")
	}

	// Past the ceiling: it leaves, whatever anybody else holds.
	clock = base.Add(91 * time.Second)
	p.flush()
	p.mu.Lock()
	held = p.held
	p.mu.Unlock()
	if held != 0 {
		t.Error("the batch was still held 91s into a 90s ceiling: max_age is a " +
			"promise, and a claim that can defer it makes it a suggestion")
	}
}

// brokenMetastore fails every call.
type brokenMetastore struct{}

func (brokenMetastore) Describe() string { return "broken" }
func (brokenMetastore) Get(context.Context, string) (string, bool, error) {
	return "", false, errBroken
}
func (brokenMetastore) Put(context.Context, string, string, time.Duration) error { return errBroken }
func (brokenMetastore) Claim(context.Context, string, time.Duration) (bool, error) {
	return false, errBroken
}
func (brokenMetastore) Incr(context.Context, string, time.Duration) (int64, error) {
	return 0, errBroken
}

var errBroken = errors.New("the metastore is down")

// refusingMetastore answers every claim with "somebody else has it".
type refusingMetastore struct{ brokenMetastore }

func (refusingMetastore) Claim(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

// With a claim, the timer wakes on the WINDOW BOUNDARY, not `every` after it
// armed.
//
// Measured against two real replicas before this existed: 8 load jobs became
// 6, not the 4 the window count says. The key was wall-clock and the alarm was
// not, so each replica counted `every` from its own arm and they contested
// different windows — r1 flushed at :40 :45 :50 :00 and r2 at :45 :55, with
// only one window genuinely shared.
//
// Aligned, every replica wakes at the same instant and asks for the same key,
// which is the only way exactly one wins per window.
func TestWithAClaimTheTimerWakesOnTheBoundary(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	for _, c := range []struct {
		name  string
		claim bool
		now   string
		want  time.Duration
	}{
		// 5s windows: 12:00:02 is 3s from the boundary at 12:00:05.
		{"claimed, mid-window", true, "2026-09-27T12:00:02Z", 3 * time.Second},
		{"claimed, just after a boundary", true, "2026-09-27T12:00:05.100Z", 4900 * time.Millisecond},
		{"claimed, on a boundary", true, "2026-09-27T12:00:05Z", 5 * time.Second},
		// Without a claim nothing contests, so the old behaviour stands: a
		// full `every` from whenever this armed.
		{"unclaimed keeps every", false, "2026-09-27T12:00:02Z", 5 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &pipe{
				stream: Stream{Buffer: Buffer{Flush: Flush{
					Every: 5 * time.Second, Claim: c.claim,
					MaxAge: Duration(time.Hour),
				}}},
				now:     func() time.Time { return at(c.now) },
				buckets: map[string]*bucket{},
			}
			if got := p.deadline(&bucket{oldest: at(c.now)}); got != c.want {
				t.Errorf("deadline = %s, want %s", got, c.want)
			}
		})
	}

	// And the ceiling still wins when it is closer than the boundary.
	p := &pipe{
		stream: Stream{Buffer: Buffer{Flush: Flush{
			Every: 5 * time.Second, Claim: true, MaxAge: Duration(10 * time.Second),
		}}},
		now:     func() time.Time { return at("2026-09-27T12:00:02Z") },
		buckets: map[string]*bucket{},
	}
	// Nine seconds old against a ten-second ceiling: one second left, which
	// beats the three to the boundary.
	old := &bucket{oldest: at("2026-09-27T12:00:02Z").Add(-9 * time.Second)}
	if got := p.deadline(old); got != time.Second {
		t.Errorf("the boundary beat the ceiling: %s, want 1s", got)
	}
}

// --- seeing the claim work, and seeing it not (issue #36, slice 5) ---------

// Four outcomes, and each is a different operational fact.
//
// A yield is NOT a flush, so it does not go in `flushes_total` -- that
// counter's sum means "batches handed to the pool" and folding a
// non-delivery into it would end that. Its own series, with the reason.
//
// The one worth an alert is `unreachable`: the claim failed open, so
// coordination is silently not happening and the load jobs are multiplying by
// replica count again. A feature whose failure mode is silent, shipped without
// the way to see it is silent, is the shape of issue #33.
func TestEveryWindowOutcomeIsCounted(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	build := func(meta Metastore, maxAge time.Duration) *pipe {
		p := newPipe(Stream{
			Name: "s",
			Buffer: Buffer{Queue: 1, Workers: 0, MaxRecords: 100, Flush: Flush{
				Every: time.Minute, Records: 1_000_000,
				Claim: true, MaxAge: Duration(maxAge),
			}},
		}, nil, nil, nil, 1<<20, NewMetrics(), meta)
		p.gateway = "g"
		p.now = func() time.Time { return base }
		return p
	}

	read := func(p *pipe, outcome string) int64 {
		var b strings.Builder
		if err := p.metrics.Render(&b); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(b.String(), "\n") {
			if strings.Contains(line, MetricWindows) && strings.Contains(line, `outcome="`+outcome+`"`) {
				f := strings.Fields(line)
				var n int64
				_, _ = fmt.Sscan(f[len(f)-1], &n)
				return n
			}
		}
		return 0
	}

	// Won: took the claim.
	shared := NewMemoryMetastore()
	won := build(shared, time.Hour)
	won.buckets[""] = &bucket{batch: []sdk.Envelope{{}}, oldest: base}
	if len(won.winners([]string{""})) != 1 {
		t.Fatal("the first asker lost an empty backend")
	}
	if n := read(won, "won"); n != 1 {
		t.Errorf(`outcome="won" = %d, want 1`, n)
	}

	// Yielded: somebody else has this window.
	lost := build(shared, time.Hour)
	lost.buckets[""] = &bucket{batch: []sdk.Envelope{{}}, oldest: base}
	if len(lost.winners([]string{""})) != 0 {
		t.Fatal("the second asker won a claimed window")
	}
	if n := read(lost, "yielded"); n != 1 {
		t.Errorf(`outcome="yielded" = %d, want 1`, n)
	}

	// Unreachable: won by failing open. Coordination is OFF and this is the
	// only thing that says so.
	down := build(brokenMetastore{}, time.Hour)
	down.buckets[""] = &bucket{batch: []sdk.Envelope{{}}, oldest: base}
	if len(down.winners([]string{""})) != 1 {
		t.Fatal("a broken backend held the batch")
	}
	if n := read(down, "unreachable"); n != 1 {
		t.Errorf(`outcome="unreachable" = %d, want 1 -- without it, a metastore `+
			`that is down looks exactly like one that is working`, n)
	}

	// Ceiling: won without asking because max_age closed. Not coordinated,
	// and correct.
	ceiling := build(refusingMetastore{}, 10*time.Second)
	ceiling.buckets[""] = &bucket{batch: []sdk.Envelope{{}}, oldest: base.Add(-time.Minute)}
	if len(ceiling.winners([]string{""})) != 1 {
		t.Fatal("the ceiling was deferred by a claim")
	}
	if n := read(ceiling, "ceiling"); n != 1 {
		t.Errorf(`outcome="ceiling" = %d, want 1`, n)
	}

	// And a stream with no claim counts nothing here at all: the series would
	// be noise on every deployment that never turned this on.
	off := newPipe(Stream{Name: "s", Buffer: Buffer{Queue: 1, Flush: Flush{Every: time.Minute}}},
		nil, nil, nil, 1<<20, NewMetrics(), shared)
	off.buckets[""] = &bucket{batch: []sdk.Envelope{{}}, oldest: base}
	_ = off.winners([]string{""})
	var b strings.Builder
	if err := off.metrics.Render(&b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), MetricWindows) {
		t.Error("a stream with no claim published window outcomes")
	}
}

// A claim on `memory` coordinates nothing, and the gateway says so at boot.
//
// It is the quietest possible misconfiguration: every flush succeeds, every
// number looks right, and the load jobs multiply by replica count exactly as
// they did before — because each replica wins its own claim. The window
// counter would show `won` every time and never `yielded`, which reads as "no
// contention" rather than "no coordination".
//
// Once per stream at startup, not per flush: a line per window would be a log
// nobody reads and a cost per batch.
func TestAClaimOnMemorySaysSoAtBoot(t *testing.T) {
	line := func(claim bool, kind string) string {
		var b strings.Builder
		cfg := &Config{
			Name: "g", Listen: Listen{Addr: ":0"},
			Streams: []Stream{{
				Name: "s", Path: "/v1/x",
				Identity: Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
				Buffer: Buffer{Flush: Flush{
					Every: time.Minute, Records: 1,
					Claim: claim, MaxAge: Duration(time.Hour),
				}},
				Sink:       Sink{Type: "fake", Metastore: MetastoreConfig{Type: kind}},
				DeadLetter: Sink{Type: "fake"},
			}},
		}
		if err := cfg.check(); err != nil {
			t.Fatal(err)
		}
		sinks := NewSinks()
		sinks.MustRegister("fake", func(Build) (Sinker, error) { return nowhereSink{}, nil })

		// The gateway logs through slog's default, so the test swaps it
		// rather than the gateway growing a WithLogger that exists for this
		// assertion and nothing else.
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn})))
		defer slog.SetDefault(prev)

		srv, err := New(cfg, nil, WithSinks(sinks))
		if err != nil {
			t.Fatal(err)
		}
		_ = srv.Close(context.Background())
		return b.String()
	}

	if got := line(true, MetastoreMemory); !strings.Contains(got, "coordinates nothing") {
		t.Errorf("a claim on memory said nothing at boot:\n%s", got)
	}
	if got := line(false, MetastoreMemory); strings.Contains(got, "coordinates nothing") {
		t.Errorf("a stream with no claim was warned about memory:\n%s", got)
	}
}

// The load-job budget is arithmetic the operator should not have to redo.
//
// `flush.every` and BigQuery's 1,500 load jobs per table per day decide
// whether a cadence fits, and today that conversion happens in somebody's
// head — or does not happen until a busy day pushes a table over. One line at
// boot turns a standing maintenance question into a fact read once.
//
// Only where a limit is KNOWN. A cadence printed for Postgres is a number with
// nothing to compare it against, and a boot log that prints those is one
// nobody reads.
func TestTheLoadJobBudgetIsSaidAtBoot(t *testing.T) {
	// `auto_table` requires authentication -- a producer that can name a table
	// can create one -- so the key has to exist for the config to pass at all.
	t.Setenv("KEYS", "k")

	boot := func(every time.Duration, into Sink) string {
		var b strings.Builder
		cfg := &Config{
			Name:   "g",
			Listen: Listen{Addr: ":0", Auth: Auth{Type: AuthBearer, KeysFrom: "KEYS"}},
			Streams: []Stream{{
				Name: "s", Path: "/s",
				Identity: Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
				Buffer:   Buffer{Flush: Flush{Every: every, Records: 1}},
				Sink: Sink{
					Type:   SinkAutoTable,
					Shape:  "columns",
					Naming: Naming{Allow: []string{"*"}},
					Into:   &into,
				},
				DeadLetter: Sink{Type: "fake"},
			}},
		}
		if err := cfg.check(); err != nil {
			t.Fatal(err)
		}
		sinks := NewSinks()
		sinks.MustRegister("fake", func(Build) (Sinker, error) { return nowhereSink{}, nil })

		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelInfo})))
		defer slog.SetDefault(prev)

		srv, err := New(cfg, nil, WithSinks(sinks), WithSink("s", nowhereSink{}))
		if err != nil {
			t.Fatal(err)
		}
		_ = srv.Close(context.Background())
		return b.String()
	}

	big := Sink{Type: SinkBigQuery, Project: "p", Dataset: "d", Write: "append"}

	// 86400/120 = 720 windows, and the limit it counts against.
	got := boot(2*time.Minute, big)
	for _, want := range []string{"720", "1500"} {
		if !strings.Contains(got, want) {
			t.Errorf("the boot line is missing %q -- the whole point is that "+
				"nobody has to do this division:\n%s", want, got)
		}
	}

	// The arithmetic, not a constant somebody pasted.
	if got := boot(time.Minute, big); !strings.Contains(got, "1440") {
		t.Errorf("every=1m did not say 1440:\n%s", got)
	}
	if got := boot(5*time.Minute, big); !strings.Contains(got, "288") {
		t.Errorf("every=5m did not say 288:\n%s", got)
	}

	// The floor `check` permits is not a comfortable place to be: 1m is 1,440
	// of the 1,500 allowed. "It started" and "it fits" are different facts,
	// and this line is what separates them.
	if got := boot(BigQueryFlushFloor, big); !strings.Contains(got, "1440") {
		t.Errorf("the smallest permitted window did not say how close to the "+
			"quota it lands:\n%s", got)
	}

	// Named, because an operator with ten streams needs to know which one.
	if got := boot(2*time.Minute, big); !strings.Contains(got, `stream=s`) {
		t.Errorf("the budget did not say which stream it belongs to:\n%s", got)
	}

	// Postgres has no such quota. A number with nothing to compare it against
	// is noise, and boot logs die of noise.
	pg := Sink{Type: SinkPostgres, DSNFrom: "DSN", Write: "append"}
	if got := boot(2*time.Minute, pg); strings.Contains(got, "load job") {
		t.Errorf("a Postgres destination was given a BigQuery budget:\n%s", got)
	}
}

// keyedSink is a sink with a routing key, which is what `auto_table` is: it
// tells the pipe how it would group a batch, and records what it was handed.
type keyedSink struct {
	mu      sync.Mutex
	batches [][]string
}

func (s *keyedSink) Describe() string                       { return "keyed" }
func (s *keyedSink) Measure(e map[string]any, _ int) string { return Text(e["t"]) }
func (s *keyedSink) Write(_ context.Context, b []sdk.Envelope) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tables []string
	for _, e := range b {
		m, _ := e.Payload.(map[string]any)
		tables = append(tables, Text(m["t"]))
	}
	s.batches = append(s.batches, tables)
	return int64(len(b)), nil
}

func (s *keyedSink) carrying(table string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.batches {
		for _, t := range b {
			if t == table {
				n++
				break
			}
		}
	}
	return n
}

// post sends one event through the real handler.
func post(t *testing.T, p *pipe, table string) {
	t.Helper()
	body := fmt.Sprintf(`{"t":%q,"k":"1","r":"2026-01-01T00:00:00Z"}`, table)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "/s", strings.NewReader(body)))
	if w.Code != http.StatusAccepted && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST %s: %d %s", table, w.Code, w.Body.String())
	}
}

func keyedPipe(sink Sinker, b Buffer) *pipe {
	// Attempts is what `check` would have defaulted; newPipe is below that
	// and a zero here makes `send` give up with a nil cause.
	return newPipe(Stream{
		Name: "s", Path: "/s", Format: FormatJSON,
		Identity: Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
		Buffer:   b, Retry: Retry{Attempts: 1},
	}, nil, sink, nowhereSink{}, 1<<20, NewMetrics(), nil)
}

// A quiet table must not inherit a busy neighbour's cadence.
//
// This is issue #38 reduced to its smallest reproduction: the buffer is per
// stream and the sink routes per table, so a producer that fills the buffer
// sets the flush frequency and every quiet table in that stream gets a load
// job each time -- carrying whatever handful of rows it happened to have.
//
// Measured by the reporter at 34x the jobs for the same data. Here: three
// events for `quiet` should leave in ONE delivery, not in three.
func TestAQuietTableDoesNotInheritTheBusyOnesCadence(t *testing.T) {
	sink := &keyedSink{}
	p := keyedPipe(sink, Buffer{
		Queue: 64, Workers: 1, MaxRecords: 100000,
		Flush: Flush{Every: time.Hour, Records: 10},
	})

	// `quiet` arrives inside three different windows of the busy table.
	for i := range 100 {
		post(t, p, "busy")
		if i == 10 || i == 40 || i == 70 {
			post(t, p, "quiet")
		}
	}
	if err := p.close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if n := sink.carrying("quiet"); n != 1 {
		t.Errorf("`quiet` left in %d deliveries, want 1 -- it has 3 events and "+
			"`flush.records` is 10, so the only thing that can flush it is its "+
			"own clock, not the neighbour's", n)
	}
	// And the busy table still flushes on its own trigger, ten at a time.
	if n := sink.carrying("busy"); n < 10 {
		t.Errorf("`busy` left in %d deliveries, want at least 10 -- 100 events "+
			"at `flush.records: 10`", n)
	}
}

// The ceiling counts the PROCESS, not the bucket.
//
// `max_bytes: 64MiB` x 94 tables is 6 GB. Splitting the trigger must not split
// the ceiling: the operator configures one number for what this process may
// hold, and a hundred quiet tables must not be able to multiply it.
func TestTheCeilingIsGlobalAcrossBuckets(t *testing.T) {
	sink := &keyedSink{}
	p := keyedPipe(sink, Buffer{
		Queue: 1, Workers: 0, MaxRecords: 50,
		// High enough that nothing flushes: the ceiling is what we are testing.
		Flush: Flush{Every: time.Hour, Records: 100000},
	})

	accepted := 0
	for i := range 80 {
		body := fmt.Sprintf(`{"t":"t%d","k":"1","r":"2026-01-01T00:00:00Z"}`, i%10)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("POST", "/s", strings.NewReader(body)))
		if w.Code == http.StatusAccepted {
			accepted++
		}
	}
	if accepted != 50 {
		t.Errorf("accepted %d events against `max_records: 50` spread over ten "+
			"tables, want 50 -- a per-bucket ceiling would have taken 500", accepted)
	}
}

// A sink with no routing key behaves exactly as it did.
//
// Most streams have one destination and must not pay for a feature they do
// not use: no Measurer means one bucket, and one bucket is today.
func TestASinkWithNoKeyIsOneBucket(t *testing.T) {
	sink := &countingSink{}
	p := keyedPipe(sink, Buffer{
		Queue: 64, Workers: 1, MaxRecords: 100000,
		Flush: Flush{Every: time.Hour, Records: 10},
	})
	for range 25 {
		post(t, p, "whatever")
	}
	if err := p.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 25 events, ten at a time: two full batches and the drain takes five.
	if got := sink.sizes(); len(got) != 3 || got[0] != 10 || got[1] != 10 || got[2] != 5 {
		t.Errorf("batches %v, want [10 10 5] -- a sink with no key must batch "+
			"exactly as it did before buckets existed", got)
	}
}

// countingSink has no Measurer, so the pipe has no key for it.
type countingSink struct {
	mu sync.Mutex
	n  []int
}

func (s *countingSink) Describe() string { return "counting" }
func (s *countingSink) Write(_ context.Context, b []sdk.Envelope) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n = append(s.n, len(b))
	return int64(len(b)), nil
}
func (s *countingSink) sizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.n...)
}

// The claim and a routing key now agree, except on memcached.
//
// Slice 1 refused the pair outright: the claim was keyed on the stream, so
// one table would take each window and the rest would yield. The claim is
// per key now and the refusal goes with it.
//
// Memcached is the exception, and it is a measurement rather than a taste:
// it has no pipelined add, so N tables cost N round trips -- all of them
// under the buffer's mutex, where admission pays for every millisecond. The
// alternative is failing open on the 500ms budget and multiplying the load
// jobs by replica count silently, which is the quietest possible
// misconfiguration and exactly what the claim exists to prevent.
func TestTheClaimNeedsABulkMetastoreForARoutingKey(t *testing.T) {
	stream := func(sink, store string) Stream {
		return Stream{
			Name: "s", Path: "/s", Format: FormatJSON,
			Identity: Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
			Buffer: Buffer{Flush: Flush{
				Every: time.Minute, Records: 10,
				Claim: true, MaxAge: Duration(time.Hour),
			}},
			Sink: Sink{
				Type: sink, Shape: "columns", Naming: Naming{Allow: []string{"*"}},
				Into:    &Sink{Type: SinkPostgres, DSNFrom: "DSN", Write: "append"},
				DSNFrom: "DSN", Table: "landing.t", Write: "append",
				Metastore: MetastoreConfig{Type: store, AddrFrom: "ADDR"},
			},
			DeadLetter: Sink{Type: SinkPostgres, DSNFrom: "DSN", Table: "landing.d", Write: "append"},
		}
	}

	// The pair slice 1 refused, now allowed.
	auto := stream(SinkAutoTable, MetastoreRedis)
	if err := auto.check(); err != nil {
		t.Errorf("`flush.claim` with `auto_table` on redis was refused, and the "+
			"per-key claim is what makes it work: %v", err)
	}

	// Memcached cannot do it in one trip, and says so.
	mc := stream(SinkAutoTable, MetastoreMemcached)
	err := mc.check()
	if err == nil {
		t.Fatal("`flush.claim` with `auto_table` on memcached was accepted: " +
			"every table is a round trip and they all hold the buffer's mutex")
	}
	for _, want := range []string{"claim", "memcached"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	// And a stream with no routing key is fine on memcached: one key, one
	// trip, which is what it always was.
	plain := stream(SinkPostgres, MetastoreMemcached)
	if err := plain.check(); err != nil {
		t.Errorf("a keyless stream was refused memcached, which is the case it "+
			"has always served: %v", err)
	}
}

// The timer takes the bucket whose clock rang, not every bucket there is.
//
// There is one timer, armed for the EARLIEST bucket, because a bucket created
// later can only be due later. When it rings, the buckets that have not
// waited their target go on filling -- otherwise the alarm one table set
// would flush all ninety-four, which is the neighbour's cadence again by a
// different road.
//
// A mutation that makes `due` always true passes every other test in this
// file: they reach the trigger through `flush.records`, and this is the only
// one that reaches it through the clock.
func TestTheTimerTakesOnlyWhatIsDue(t *testing.T) {
	const every = 300 * time.Millisecond
	sink := &keyedSink{}
	p := keyedPipe(sink, Buffer{
		Queue: 64, Workers: 1, MaxRecords: 1000,
		Flush: Flush{Every: every, Records: 1000},
	})

	post(t, p, "early")
	time.Sleep(200 * time.Millisecond)
	post(t, p, "late") // due at 500ms, not at 300ms

	// 400ms: `early` has flushed on its own clock; `late` has 100ms to go.
	time.Sleep(200 * time.Millisecond)
	if n := sink.carrying("early"); n != 1 {
		t.Errorf("`early` left in %d deliveries at 400ms, want 1", n)
	}
	if n := sink.carrying("late"); n != 0 {
		t.Errorf("`late` left in %d deliveries at 400ms, want 0 -- it was 200ms "+
			"old when the neighbour's timer rang, and its own target is 300ms", n)
	}

	// 650ms: past 200+300, so `late` goes on its own clock.
	time.Sleep(250 * time.Millisecond)
	if n := sink.carrying("late"); n != 1 {
		t.Errorf("`late` left in %d deliveries at 650ms, want 1 -- a bucket the "+
			"timer skipped still has to leave when its own target passes", n)
	}
	if err := p.close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The claim follows the key, because a per-stream claim couples the tables
// again -- one level further down than issue #38 found them.
//
// Two replicas behind a load balancer do not hold the same tables: whichever
// producer hit which pod decides. Replica A holds `clicks`, replica B holds
// `orders`. With ONE claim for the stream, A wins the window and B yields --
// so `orders` waits a whole window for a claim `clicks` took, and with N
// replicas it can wait N of them. `max_age` bounds that, which is not the
// same as it not happening.
//
// Per key they claim different things and both leave. Slice 1 refused the
// pair for this; this is the reason going away.
func TestTheClaimIsPerKeyNotPerStream(t *testing.T) {
	shared := NewMemoryMetastore()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	replica := func() *pipe {
		p := newPipe(Stream{
			Name: "s",
			Buffer: Buffer{Queue: 8, Workers: 0, MaxRecords: 100, Flush: Flush{
				Every: time.Minute, Records: 1_000_000,
				Claim: true, MaxAge: Duration(time.Hour),
			}},
		}, nil, nil, nil, 1<<20, NewMetrics(), shared)
		p.gateway = "g"
		p.now = func() time.Time { return base }
		return p
	}

	a, b := replica(), replica()
	for _, c := range []struct {
		p     *pipe
		table string
	}{{a, "clicks"}, {b, "orders"}} {
		if err := c.p.enqueue(prepared{
			events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{c.table},
		}); err != nil {
			t.Fatal(err)
		}
	}

	a.flush()
	b.flush()

	for _, c := range []struct {
		p     *pipe
		table string
	}{{a, "clicks"}, {b, "orders"}} {
		c.p.mu.Lock()
		_, held := c.p.buckets[c.table]
		c.p.mu.Unlock()
		if held {
			t.Errorf("%s was still held: the replica that has it lost the "+
				"window to a replica holding a different table, so one table's "+
				"cadence is still another's", c.table)
		}
	}
}

// And the same stream with no routing key still gets exactly one flush per
// window however many replicas hold it -- issue #36, unchanged.
func TestTheKeylessClaimIsUnchanged(t *testing.T) {
	shared := NewMemoryMetastore()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	replica := func() *pipe {
		p := newPipe(Stream{
			Name: "s",
			Buffer: Buffer{Queue: 4, Workers: 0, MaxRecords: 100, Flush: Flush{
				Every: time.Minute, Records: 1_000_000,
				Claim: true, MaxAge: Duration(time.Hour),
			}},
		}, nil, nil, nil, 1<<20, NewMetrics(), shared)
		p.gateway = "g"
		p.now = func() time.Time { return base }
		return p
	}

	flushed := 0
	for _, p := range []*pipe{replica(), replica(), replica()} {
		if err := p.enqueue(prepared{
			events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{""},
		}); err != nil {
			t.Fatal(err)
		}
		p.flush()
		p.mu.Lock()
		if len(p.buckets) == 0 {
			flushed++
		}
		p.mu.Unlock()
	}
	if flushed != 1 {
		t.Errorf("%d of 3 keyless replicas flushed one window, want 1", flushed)
	}
}

// bulkStore counts round trips, so "one call for ninety-four keys" is a fact
// and not a hope. Slice 0 measured 94 sequential claims at 2.589s of held
// mutex against a Redis at 25ms RTT, and 29.8ms pipelined -- the same as ONE
// claim costs today.
type bulkStore struct {
	Metastore
	mu    sync.Mutex
	trips int
	seen  []string
}

func (s *bulkStore) ClaimMany(ctx context.Context, keys []string, ttl time.Duration) ([]bool, error) {
	s.mu.Lock()
	s.trips++
	s.seen = append(s.seen, keys...)
	s.mu.Unlock()
	out := make([]bool, len(keys))
	for i, k := range keys {
		got, err := s.Claim(ctx, k, ttl)
		if err != nil {
			return nil, err
		}
		out[i] = got
	}
	return out, nil
}

// A metastore that can claim in bulk is asked ONCE, however many tables are
// due.
//
// This is the whole finding of slice 0: the objection to a per-key claim was
// 94 round trips holding `p.mu`, and a pipeline makes it one. Sequential, the
// admission p99 tracked the hold time 1:1 -- 2.589s at 25ms RTT.
func TestABulkMetastoreIsAskedOnce(t *testing.T) {
	store := &bulkStore{Metastore: NewMemoryMetastore()}
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	p := newPipe(Stream{
		Name: "s",
		Buffer: Buffer{Queue: 32, Workers: 0, MaxRecords: 1000, Flush: Flush{
			Every: time.Minute, Records: 1_000_000,
			Claim: true, MaxAge: Duration(time.Hour),
		}},
	}, nil, nil, nil, 1<<20, NewMetrics(), store)
	p.gateway = "g"
	p.now = func() time.Time { return base }

	for i := range 20 {
		if err := p.enqueue(prepared{
			events: []sdk.Envelope{{}}, sizes: []int64{1},
			keys: []string{fmt.Sprintf("t%02d", i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	p.flush()

	store.mu.Lock()
	trips, seen := store.trips, len(store.seen)
	store.mu.Unlock()
	if trips != 1 {
		t.Errorf("%d round trips for 20 tables, want 1 -- sequential claims hold "+
			"`p.mu` for their whole duration, and admission pays every "+
			"millisecond of it", trips)
	}
	if seen != 20 {
		t.Errorf("the one call carried %d keys, want 20", seen)
	}
}

// An unreachable metastore fails OPEN, per key.
//
// Losing coordination costs load jobs; refusing to flush holds data that was
// already answered 202. The direction was chosen when the claim landed and it
// does not change because the key did.
func TestAnUnreachableMetastoreLetsEveryKeyThrough(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	p := newPipe(Stream{
		Name: "s",
		Buffer: Buffer{Queue: 8, Workers: 0, MaxRecords: 100, Flush: Flush{
			Every: time.Minute, Records: 1_000_000,
			Claim: true, MaxAge: Duration(time.Hour),
		}},
	}, nil, nil, nil, 1<<20, NewMetrics(), brokenMetastore{})
	p.gateway = "g"
	p.now = func() time.Time { return base }

	for _, table := range []string{"a", "b", "c"} {
		if err := p.enqueue(prepared{
			events: []sdk.Envelope{{}}, sizes: []int64{1}, keys: []string{table},
		}); err != nil {
			t.Fatal(err)
		}
	}
	p.flush()

	p.mu.Lock()
	held := len(p.buckets)
	p.mu.Unlock()
	if held != 0 {
		t.Errorf("%d buckets held after an unreachable metastore, want 0 -- "+
			"refusing to flush holds events somebody was already told were "+
			"accepted", held)
	}

	var b strings.Builder
	if err := p.metrics.Render(&b); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(b.String(), UnreachableWindow); n == 0 {
		t.Error("an unreachable metastore was not counted, so a claim that " +
			"coordinates nothing looks exactly like one that works")
	}
}

// Flushes per TABLE, which is the number the quota is actually about.
//
// `brevis_gateway_flushes_total{stream,trigger}` counts batches handed to the
// pool, and with a routing key one batch is one table -- but the counter
// cannot say WHICH, so a stream at 94 tables reports one number against a
// limit that is per table. BigQuery allows 1,500 load jobs per table per day;
// an operator alerting at 1,200 needs the table.
//
// Behind BREVIS_INGESTION_METRICS, beside the volume pair, and for the same
// reason: `table` is a label the PRODUCER chooses. The boot line is the
// unconditional half of this -- one line, no cardinality, saying what the
// cadence costs before an event arrives.
func TestFlushesAreCountedPerTableWhenAsked(t *testing.T) {
	flush := func() string {
		sink := &keyedSink{}
		p := keyedPipe(sink, Buffer{
			Queue: 64, Workers: 1, MaxRecords: 100000,
			Flush: Flush{Every: time.Hour, Records: 2},
		})
		for range 2 {
			post(t, p, "clicks")
		}
		for range 2 {
			post(t, p, "orders")
		}
		if err := p.close(context.Background()); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		if err := p.metrics.Render(&b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("off by default", func(t *testing.T) {
		t.Setenv(EnvIngestionMetrics, "")
		got := flush()
		if strings.Contains(got, MetricTableFlushes) {
			t.Errorf("the per-table series appeared with the switch off:\n%s", got)
		}
		// And the stream-level counter is still there, unchanged.
		if !strings.Contains(got, MetricFlushes) {
			t.Errorf("the stream's flush counter went missing:\n%s", got)
		}
	})

	t.Run("on when asked", func(t *testing.T) {
		t.Setenv(EnvIngestionMetrics, "true")
		got := flush()
		for _, want := range []string{
			MetricTableFlushes + `{stream="s",table="clicks",trigger="records"}`,
			MetricTableFlushes + `{stream="s",table="orders",trigger="records"}`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %s\n%s", want, got)
			}
		}
		// The stream-level counter keeps its own shape: no `table` label, so
		// a deployment that never turns this on sees the cardinality it
		// always had.
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, MetricFlushes+"{") && strings.Contains(line, "table=") {
				t.Errorf("the stream counter grew a `table` label: %s", line)
			}
		}
	})
}

// concurrentStore records how many claims were in flight at once, and can be
// told which keys win.
type concurrentStore struct {
	Metastore
	mu       sync.Mutex
	inflight int
	peak     int
	seen     []string
	delay    time.Duration
	fail     string // the key that errors, if any
}

func (s *concurrentStore) Claim(_ context.Context, key string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	s.inflight++
	if s.inflight > s.peak {
		s.peak = s.inflight
	}
	s.seen = append(s.seen, key)
	s.mu.Unlock()

	time.Sleep(s.delay)

	s.mu.Lock()
	s.inflight--
	s.mu.Unlock()

	if key == s.fail {
		return false, errBroken
	}
	// Odd keys win, so a caller that shuffles the answers is caught by the
	// pattern rather than by a count.
	n, _ := strconv.Atoi(strings.TrimPrefix(key, "k"))
	return n%2 == 1, nil
}

// The fallback answers per key, IN ORDER, however it ran them.
//
// The Redis pipeline has this test because results read out of order hand a
// table a window another table won -- silently, with every flush still
// looking like a flush. A fan-out can make the same mistake more easily: the
// goroutines finish in whatever order they finish.
func TestTheClaimFallbackAnswersInOrder(t *testing.T) {
	store := &concurrentStore{Metastore: NewMemoryMetastore(), delay: time.Millisecond}
	keys := make([]string, 40)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}

	got, err := claimAll(context.Background(), store, keys, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(keys) {
		t.Fatalf("%d answers for %d keys", len(got), len(keys))
	}
	for i, won := range got {
		if want := i%2 == 1; won != want {
			t.Errorf("key %d: won=%v, want %v -- the answers do not line up "+
				"with the keys, so a table can be handed a window another "+
				"table took", i, won, want)
		}
	}
}

// And it runs them in parallel, bounded.
//
// Sequential was the whole objection to a per-key claim: 94 claims one at a
// time cost 2.589s of held mutex against a Redis at 25ms RTT, and admission's
// p99 tracked it exactly. Bounded, because a stream at 10,000 tables must not
// open 10,000 connections to find out who won.
func TestTheClaimFallbackRunsInParallelWithinABound(t *testing.T) {
	store := &concurrentStore{Metastore: NewMemoryMetastore(), delay: 5 * time.Millisecond}
	keys := make([]string, 200)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}

	start := time.Now()
	if _, err := claimAll(context.Background(), store, keys, time.Minute); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)

	store.mu.Lock()
	peak := store.peak
	store.mu.Unlock()

	if peak <= 1 {
		t.Errorf("peak concurrency %d: the fallback is still a loop, and every "+
			"backend without a bulk primitive pays a round trip per table with "+
			"the buffer held", peak)
	}
	if peak > claimFanout {
		t.Errorf("peak concurrency %d exceeds the bound of %d", peak, claimFanout)
	}
	// 200 keys at 5ms, in waves of claimFanout. Sequential would be a second.
	if want := time.Duration(len(keys)/claimFanout+2) * 5 * time.Millisecond; took > 4*want {
		t.Errorf("200 claims took %s, want around %s", took.Round(time.Millisecond), want)
	}
}

// One key takes no goroutine at all: the timer path on a stream with one
// destination is the common case and must not pay for machinery it cannot use.
func TestASingleClaimDoesNotFanOut(t *testing.T) {
	store := &concurrentStore{Metastore: NewMemoryMetastore()}
	if _, err := claimAll(context.Background(), store, []string{"k1"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	peak := store.peak
	store.mu.Unlock()
	if peak != 1 {
		t.Errorf("peak concurrency %d for one key", peak)
	}
}

// An error from any key fails the whole call, and the caller fails open for
// all of them -- the direction chosen when the claim landed.
func TestTheClaimFallbackReportsAnError(t *testing.T) {
	store := &concurrentStore{Metastore: NewMemoryMetastore(), fail: "k7"}
	keys := make([]string, 20)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	if _, err := claimAll(context.Background(), store, keys, time.Minute); err == nil {
		t.Error("a backend that errored on one key reported success for all of them")
	}
}
