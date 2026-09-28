package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
		queue: make(chan []sdk.Envelope),
	}

	if err := p.enqueue([]sdk.Envelope{{}, {}}, []int64{10, 20}); err != nil {
		t.Fatal(err)
	}
	if p.bytes != 30 {
		t.Fatalf("bytes = %d after 10+20", p.bytes)
	}

	p.mu.Lock()
	p.handoff(TriggerSize)
	p.mu.Unlock()

	if p.bytes != 30 {
		t.Errorf("bytes = %d after the batch came back: the ceiling forgot %d "+
			"bytes it is still holding", p.bytes, 30-p.bytes)
	}
	if len(p.sizes) != len(p.batch) {
		t.Errorf("%d sizes for %d events: the two have to stay parallel or the "+
			"next handoff hands back the wrong total", len(p.sizes), len(p.batch))
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
	}
	if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.handoff(TriggerSize)
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
		every, maxAge time.Duration
		age           time.Duration // how old the batch already is
		want          time.Duration
	}{
		{"no max_age: every, always", time.Minute, 0, 10 * time.Minute, time.Minute},
		{"fresh batch: every wins", time.Minute, 5 * time.Minute, 0, time.Minute},
		{"aging batch, still room", time.Minute, 5 * time.Minute, 3 * time.Minute, time.Minute},
		// 280s old against a 300s ceiling: 20s left, which is less than the
		// 60s target, so the ceiling decides.
		{"the ceiling closes in", time.Minute, 5 * time.Minute, 280 * time.Second, 20 * time.Second},
		{"ceiling already passed: now", time.Minute, 5 * time.Minute, 10 * time.Minute, 0},
		// An empty buffer has no oldest event, so there is nothing to age.
		{"nothing held: every", time.Minute, 5 * time.Minute, -1, time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &pipe{
				stream: Stream{Buffer: Buffer{Flush: Flush{
					Every: c.every, MaxAge: Duration(c.maxAge),
				}}},
				now: func() time.Time { return base },
			}
			if c.age >= 0 {
				p.oldest = base.Add(-c.age)
			}
			if got := p.deadline(); got != c.want {
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
		queue: make(chan []sdk.Envelope, 1),
		now:   func() time.Time { return clock },
	}

	if !p.oldest.IsZero() {
		t.Fatal("an empty buffer has an oldest event")
	}
	if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if !p.oldest.Equal(base) {
		t.Fatalf("oldest = %s, want the moment the first event arrived (%s)", p.oldest, base)
	}

	// More events later must not reset the clock: the ceiling belongs to the
	// event that has waited longest, not to the newest one.
	clock = base.Add(30 * time.Second)
	if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if !p.oldest.Equal(base) {
		t.Errorf("oldest moved to %s when a later event arrived", p.oldest)
	}

	// And taking the batch clears it, because what is left is nothing.
	p.mu.Lock()
	_, _ = p.take()
	p.mu.Unlock()
	if !p.oldest.IsZero() {
		t.Errorf("oldest survived take() as %s", p.oldest)
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
	}
	p.batch = []sdk.Envelope{{}}
	p.sizes = []int64{1}
	// Already 50ms old: about 10ms of ceiling left.
	p.oldest = time.Now().Add(-50 * time.Millisecond)

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
		if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
		// flush() takes the lock itself; holding it here deadlocks.
		p.flush()
		p.mu.Lock()
		held := len(p.batch)
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
		if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
			t.Fatal(err)
		}
		p.flush()
		p.mu.Lock()
		held := len(p.batch)
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

	if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	p.flush()

	p.mu.Lock()
	held := len(p.batch)
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

	if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
		t.Fatal(err)
	}

	// Inside the ceiling: it asks, it loses, it keeps filling.
	clock = base.Add(time.Minute)
	p.flush()
	p.mu.Lock()
	held := len(p.batch)
	p.mu.Unlock()
	if held == 0 {
		t.Fatal("it flushed while the ceiling still had room and the claim was refused")
	}

	// Past the ceiling: it leaves, whatever anybody else holds.
	clock = base.Add(91 * time.Second)
	p.flush()
	p.mu.Lock()
	held = len(p.batch)
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
				now: func() time.Time { return at(c.now) },
			}
			p.oldest = at(c.now)
			if got := p.deadline(); got != c.want {
				t.Errorf("deadline = %s, want %s", got, c.want)
			}
		})
	}

	// And the ceiling still wins when it is closer than the boundary.
	p := &pipe{
		stream: Stream{Buffer: Buffer{Flush: Flush{
			Every: 5 * time.Second, Claim: true, MaxAge: Duration(10 * time.Second),
		}}},
		now: func() time.Time { return at("2026-09-27T12:00:02Z") },
	}
	// Nine seconds old against a ten-second ceiling: one second left, which
	// beats the three to the boundary.
	p.oldest = at("2026-09-27T12:00:02Z").Add(-9 * time.Second)
	if got := p.deadline(); got != time.Second {
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
	if !won.winsTheWindow() {
		t.Fatal("the first asker lost an empty backend")
	}
	if n := read(won, "won"); n != 1 {
		t.Errorf(`outcome="won" = %d, want 1`, n)
	}

	// Yielded: somebody else has this window.
	lost := build(shared, time.Hour)
	if lost.winsTheWindow() {
		t.Fatal("the second asker won a claimed window")
	}
	if n := read(lost, "yielded"); n != 1 {
		t.Errorf(`outcome="yielded" = %d, want 1`, n)
	}

	// Unreachable: won by failing open. Coordination is OFF and this is the
	// only thing that says so.
	down := build(brokenMetastore{}, time.Hour)
	if !down.winsTheWindow() {
		t.Fatal("a broken backend held the batch")
	}
	if n := read(down, "unreachable"); n != 1 {
		t.Errorf(`outcome="unreachable" = %d, want 1 -- without it, a metastore `+
			`that is down looks exactly like one that is working`, n)
	}

	// Ceiling: won without asking because max_age closed. Not coordinated,
	// and correct.
	ceiling := build(refusingMetastore{}, 10*time.Second)
	ceiling.oldest = base.Add(-time.Minute)
	if !ceiling.winsTheWindow() {
		t.Fatal("the ceiling was deferred by a claim")
	}
	if n := read(ceiling, "ceiling"); n != 1 {
		t.Errorf(`outcome="ceiling" = %d, want 1`, n)
	}

	// And a stream with no claim counts nothing here at all: the series would
	// be noise on every deployment that never turned this on.
	off := newPipe(Stream{Name: "s", Buffer: Buffer{Queue: 1, Flush: Flush{Every: time.Minute}}},
		nil, nil, nil, 1<<20, NewMetrics(), shared)
	_ = off.winsTheWindow()
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
