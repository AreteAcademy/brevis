package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// sendTimeout bounds one delivery, retries included. Past it the batch goes to
// the dead letter: a send that has been trying for a minute is a sink that is
// down, and holding the events in memory while it is down is how memory becomes
// the outage.
const sendTimeout = 60 * time.Second

// startupTimeout bounds building the sinks. Thirty seconds is long for a
// credential fetch and short against any readiness probe worth having.
const startupTimeout = 30 * time.Second

// archiveTimeout bounds the one piece of I/O that happens on a request
// goroutine. Ten seconds is long for a single object write and short enough
// that a broken archive does not become the caller's outage.
const archiveTimeout = 10 * time.Second

// Server is the gateway listening.
type Server struct {
	cfg     *Config
	mux     *http.ServeMux
	pipes   []*pipe
	keys    []string
	metrics *Metrics
}

// Metrics is what this gateway has counted, for a caller that wants to serve
// it. Main does; a consumer embedding the gateway in something larger may
// want it on a mux of their own.
func (s *Server) Metrics() *Metrics { return s.metrics }

// Option adjusts a Server at construction.
type Option func(*options)

type options struct {
	sinks      map[string]Sinker
	catalog    *Sinks
	stores     *Stores
	metastores *Metastores
	meta       map[string]Metastore
}

// WithSinks says which destinations this binary carries.
//
// Without it a gateway registers none and refuses every stream by name, which
// is the honest default for a library: the sinks are Go, compiled in, and only
// the main that built the binary knows which ones it linked. cmd/gateway
// registers all six; cmd/gateway-slim registers two.
func WithSinks(c *Sinks) Option { return func(o *options) { o.catalog = c } }

// WithMetastores says which metastore backends this binary carries.
//
// `memory` is always available and needs no registration: a gateway that
// cannot start without Redis is a gateway with a new hard dependency for a
// cache. Registering redis or memcached is what makes several replicas behave
// like one -- see gateway.Metastore.
func WithMetastores(m *Metastores) Option { return func(o *options) { o.metastores = m } }

// WithStores says which object-store backends this binary carries, by scheme.
//
// Separate from WithSinks because a scheme is not a destination: `files` is one
// sink that writes to a directory, to gs:// and to s3://, and a build that
// only ever writes locally should not carry the AWS SDK to do it.
func WithStores(s *Stores) Option { return func(o *options) { o.stores = s } }

// WithSink replaces one stream's destination.
//
// It exists for the reason the SDK's drivers carry a Client field: "a publish
// that fails halfway is the case this driver is built around, and it cannot be
// produced on demand against a real topic". A sink that refuses, refuses twice
// then works, or is simply unreachable is exactly what the retry and the dead
// letter are for, and none of it can be arranged against a real one.
//
// It is also the seam for a destination this does not ship: Sinker is exported,
// and a consumer with one of their own needs no fork.
func WithSink(stream string, s Sinker) Option {
	return func(o *options) {
		if o.sinks == nil {
			o.sinks = map[string]Sinker{}
		}
		o.sinks[stream] = s
	}
}

// New wires a config to the hooks this binary carries.
//
// Everything that can fail does so HERE and not on a request: an unknown hook,
// an unknown sink, a bad path. A gateway that starts on a file it half
// understood drops events for a reason nobody can see.
func New(cfg *Config, hooks *Hooks, opts ...Option) (*Server, error) {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	if hooks == nil {
		hooks = NewHooks()
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), metrics: NewMetrics()}

	// Bounded, because building a cloud client resolves credentials and that
	// can hang: an unreachable metadata server leaves storage.NewClient
	// waiting, and a pod hung in New is one that never reports why. Past this
	// the startup fails and says which sink it was on.
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	for i := range cfg.Streams {
		st := cfg.Streams[i]

		var hook Hook
		if st.Hook != "" {
			fn, err := hooks.get(st.Hook)
			if err != nil {
				return nil, fmt.Errorf("stream %q: %w", st.Name, err)
			}
			hook = fn
		}

		sink, ok := o.sinks[st.Name]
		if !ok {
			var err error
			if sink, err = o.build(ctx, st.Sink, st.Name, cfg.Name); err != nil {
				return nil, fmt.Errorf("stream %q: %w", st.Name, err)
			}
		}
		dead, err := o.build(ctx, st.DeadLetter, st.Name, cfg.Name)
		if err != nil {
			return nil, fmt.Errorf("stream %q: dead_letter: %w", st.Name, err)
		}

		var big *oversize
		if ov := st.Oversize; ov != nil {
			archive, err := o.build(ctx, ov.Archive, st.Name, cfg.Name)
			if err != nil {
				return nil, fmt.Errorf("stream %q: oversize.archive: %w", st.Name, err)
			}
			big = &oversize{limit: int(ov.LargerThan), archive: archive}
			if ov.Hook != "" {
				fn, err := hooks.get(ov.Hook)
				if err != nil {
					return nil, fmt.Errorf("stream %q: oversize.hook: %w", st.Name, err)
				}
				big.reduce = fn
			}
		}

		// The stream's metastore, resolved by the same cache the sink used, so
		// the router and the pipe share one connection rather than opening two
		// to say the same thing. Nil only if opening it failed, which build
		// already refused above.
		meta := o.meta[st.Name]

		p := newPipe(st, hook, sink, dead, int64(cfg.Listen.MaxBody), s.metrics, meta)
		p.gateway = cfg.Name

		// The quietest possible misconfiguration, said once, where somebody
		// is watching.
		//
		// A claim on `memory` succeeds every time -- each replica wins its own
		// -- so every flush looks right and the load jobs multiply by replica
		// count exactly as they did before. The window counter would read
		// `won` forever and never `yielded`, which looks like no contention
		// rather than no coordination.
		//
		// Once per stream at boot and not per flush: a line per window is a
		// log nobody reads and a cost per batch.
		if st.Buffer.Flush.Claim && st.Sink.Metastore.Type == MetastoreMemory {
			slog.Warn("the flush claim is on and this stream's metastore is "+
				"`memory`, which coordinates nothing: every replica wins its own "+
				"claim, so the load jobs still multiply by replica count. Point "+
				"it at redis or memcached",
				"stream", st.Name, "metastore", MetastoreMemory)
		}

		// The load-job budget, said once where somebody is watching.
		//
		// `check` already refuses a window too small to hold the quota -- see
		// BigQueryFlushFloor -- and this is the SAME arithmetic said when the
		// config passes. They are not the same fact: the smallest window the
		// gateway permits already spends 1,440 of the 1,500 allowed, so
		// "it started" is a long way from "it fits".
		//
		// Only where a limit is known. A cadence printed for Postgres is a
		// number with nothing to compare it against, and a boot log full of
		// those is one nobody reads.
		//
		// Once per stream at boot and not per flush, for the reason above it.
		if every := st.Buffer.Flush.Every; every > 0 && st.Sink.usesBigQuery() {
			slog.Info("a table with traffic in every window costs one load job per "+
				"window, and `flush.records` or `flush.size` firing only adds to it",
				"stream", st.Name, "every", every,
				"load_jobs_per_table_per_day", int(24*time.Hour/every),
				"bigquery_allows", BigQueryDailyLoadJobs)
		}
		p.big = big
		s.pipes = append(s.pipes, p)
		s.mux.Handle("POST "+st.Path, p)
	}

	// /health is outside the guard: a readiness probe has no credential, and
	// one that needed a token would report the gateway down whenever the token
	// was wrong -- which is a different outage from the one it exists to see.
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})

	keys, err := cfg.Listen.Auth.keys()
	if err != nil {
		return nil, err
	}
	s.keys = keys

	// The gauges are read off the pipes at scrape time rather than recorded
	// per event: a buffer depth written on every request is a write per event
	// to report a number nobody reads between scrapes.
	pipes := s.pipes
	s.metrics.depth = func() []gauge {
		out := make([]gauge, 0, len(pipes)*2)
		for _, p := range pipes {
			p.mu.Lock()
			buffered := p.held
			p.mu.Unlock()
			out = append(out,
				gauge{name: MetricBuffer, labels: []string{p.stream.Name}, value: int64(buffered)},
				gauge{name: MetricQueue, labels: []string{p.stream.Name}, value: int64(len(p.queue))},
			)
		}
		return out
	}
	return s, nil
}

// build resolves one sink through the registry. It is the only place a type
// name becomes an implementation, so an unknown one cannot reach the request
// path -- and everything that can fail fails HERE: a missing connection
// string, absent cloud credentials, a staging prefix nobody can write. A
// gateway that goes ready and discovers this on the first batch is a gateway
// that loses it.
func (o *options) build(ctx context.Context, s Sink, stream, gateway string) (Sinker, error) {
	if o.catalog == nil {
		o.catalog = NewSinks()
	}
	meta, err := o.metastoreFor(ctx, stream, s)
	if err != nil {
		return nil, err
	}
	return BuildSink(Build{Ctx: ctx, Sink: s, Stores: o.stores, Sinks: o.catalog,
		Stream: stream, Gateway: gateway, Meta: meta})
}

// metastoreFor opens one backend per stream, once.
//
// Per stream and not per sink: a stream's router and the sink it routes into
// share the state, and opening two connections to say the same thing is two
// things to watch.
func (o *options) metastoreFor(ctx context.Context, stream string, s Sink) (Metastore, error) {
	if o.meta == nil {
		o.meta = map[string]Metastore{}
	}
	if m, ok := o.meta[stream]; ok {
		return m, nil
	}
	m, err := o.metastores.Open(ctx, s.Metastore)
	if err != nil {
		return nil, err
	}
	o.meta[stream] = m
	return m, nil
}

// Handler is the gateway's routes, behind the guard.
//
// The guard wraps the MUX and not each stream: a path that is not a stream must
// answer 401 as well, or an unauthenticated caller learns which paths exist by
// reading the status codes.
func (s *Server) Handler() http.Handler {
	if len(s.keys) == 0 {
		return s.mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			s.mux.ServeHTTP(w, r)
			return
		}
		guard(s.keys, s.mux).ServeHTTP(w, r)
	})
}

// Close drains every stream. What is in a buffer at shutdown is delivered, not
// dropped: `memory` already loses on a crash, and losing on a clean stop as
// well would make the tier useless rather than merely limited.
func (s *Server) Close(ctx context.Context) error {
	// Every stream, and every failure -- not the first.
	//
	// One stream running out of budget says nothing about the others, and an
	// operator reading "stream A lost 12" while stream B silently lost 4,000
	// is worse served than by both lines.
	var failures []error
	for _, p := range s.pipes {
		if err := p.close(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Pending is how many accepted events have not been delivered, across every
// stream. Zero after a clean drain.
func (s *Server) Pending() int64 {
	var n int64
	for _, p := range s.pipes {
		n += p.pending.Load()
	}
	return n
}

// oversize is the claim-check path, resolved.
type oversize struct {
	limit   int
	archive Sinker
	reduce  Hook
}

// errSaturated is the buffer at its ceiling: the sink is behind and this
// stream is holding all it agreed to hold.
//
// A distinct error and not a generic one because it is the only failure here
// that is about CAPACITY rather than about the request, and the caller is meant
// to do something different about it -- wait and send the same body again.
var errSaturated = errors.New("saturated")

// pipe is one stream: the handler, its buffer, and the pool that delivers.
//
// The request goroutine never delivers. It decodes, runs the hook, computes the
// identity and appends to a slice; a full batch is handed to `queue` and picked
// up by a worker. That is the whole of the asynchrony, and it is what keeps a
// sink's latency out of the caller's: a Pub/Sub publish that takes 40 ms, a
// COPY that takes 200, a sink that is down and burns the full retry window --
// none of it is time a client spends waiting.
//
// What it is NOT is fire-and-forget. The queue is bounded, and when it and the
// buffer are both full the gateway answers 503 rather than accepting an event
// it has nowhere to put. An accepted event that is never delivered is the one
// outcome this service exists to not have.
type pipe struct {
	stream  Stream
	hook    Hook
	sink    Sinker
	dead    Sinker
	maxBody int64

	queue chan []sdk.Envelope
	wg    sync.WaitGroup
	// pending is how many accepted events have not been delivered yet.
	//
	// It exists for the drain's error message. "context deadline exceeded"
	// does not tell an operator whether they lost four events or forty
	// thousand, and that number is the difference between a note and an
	// incident.
	pending atomic.Int64
	metrics *Metrics

	// meta is the stream's metastore, and the pipe holds it because the flush
	// claim is taken on the TIMER path -- issue #36. Nil means nothing
	// coordinates, which is also what `memory` does: every replica wins its
	// own claim.
	meta Metastore

	// gateway is cfg.Name, and it is in the claim's key.
	//
	// Without it two DIFFERENT deployments with a stream both called `tables`,
	// sharing one Redis, would coordinate with each other -- each halving the
	// other's flushes for no reason, or for the wrong one. Replicas of one
	// deployment share it because they share the file it came from.
	gateway string

	big     *oversize
	admit   Admitter
	measure Measurer

	mu sync.Mutex

	// buckets is the buffer, split by the sink's ROUTING KEY -- the table, for
	// auto_table -- and keyed by "" when the sink has none.
	//
	// The trigger has to measure what the destination carries. A buffer per
	// stream with a sink that routes per table means a producer filling the
	// buffer sets the flush frequency, and every quiet table in that stream
	// gets a load job each time, carrying whatever handful of rows it had:
	// 34x the load jobs for the same data, measured by the consumer who
	// reported issue #38. `flush.size: 8MiB` then means 8 MiB of the STREAM,
	// which is a quantity no destination ever receives.
	//
	// The key arrives free. Measurer already returns it once per event, at
	// admission, and the pipe used to spend it on a metric and drop it.
	buckets map[string]*bucket

	// held and bytes are the totals ACROSS buckets, because the ceiling is
	// the process's and not the key's. `max_bytes: 64MiB` times 94 tables is
	// 6 GB: split the trigger, never the ceiling.
	//
	// Kept as they arrive rather than summed per request: the ceiling is
	// checked on every enqueue, and walking the buckets each time would make
	// an O(1) admission O(n) in what is already held.
	held  int
	bytes int64

	// gen numbers the enqueues, and pairs with bucket.mark.
	gen uint64

	// now is a field so a test can move the clock. The router has one for the
	// same reason.
	now func() time.Time

	timer   *time.Timer
	closing bool
}

// bucket is one routing key's share of the buffer.
//
// `oldest` is when the FIRST event of THIS bucket arrived, and it is what
// both the target and `flush.max_age` are measured from -- the first and not
// the newest, because the wait belongs to the event that has waited longest.
type bucket struct {
	batch  []sdk.Envelope
	sizes  []int64
	bytes  int64
	oldest time.Time

	// mark is the enqueue that last touched this bucket, so collecting the
	// keys one request filled costs a comparison per event rather than a
	// scan of the ones already collected.
	//
	// By construction rather than by measurement: the scan was O(tables in
	// this body), which grows with exactly the thing this feature exists to
	// support, and the machine this was written on could not resolve the
	// difference -- its run-to-run spread on the same binary is wider than
	// the effect.
	mark uint64
}

// deadline is how long the timer should wait: the earlier of the target and
// what the ceiling has left.
//
// With no `max_age`, or with nothing held, it is `every` -- today's behaviour
// exactly. With a ceiling and an aging batch it shrinks, and once the ceiling
// has passed it is zero, which fires immediately.
//
// Until the flush claim lands (issue #36) nothing defers the target, so `every`
// always wins and this is arithmetic nobody reaches. It is pinned now because
// slice 3 consults it and a deadline computed wrong there holds data.
func (p *pipe) deadline(b *bucket) time.Duration {
	every := p.stream.Buffer.Flush.Every
	now := p.clock()

	// The target is measured from THIS bucket's oldest event, which is what
	// the single batch's timer used to mean when there was one batch. A
	// bucket that started later waits later, which is the whole point: a
	// quiet table must not leave because a busy one filled up.
	wait := every
	if b != nil && !b.oldest.IsZero() {
		wait = b.oldest.Add(every).Sub(now)
	}

	// With a claim, wake on the WINDOW BOUNDARY rather than `every` after
	// arming. The claim's key is wall-clock; without this the alarm is not,
	// so each replica counts `every` from its own arm and they contest
	// different windows.
	//
	// Measured on two replicas before this existed: 8 load jobs became 6, not
	// the 4 the window count says -- r1 fired at :40 :45 :50 :00 and r2 at
	// :45 :55, sharing one window out of four. Aligned, every replica wakes at
	// the same instant and asks for the same key, which is the only way
	// exactly one wins.
	if p.stream.Buffer.Flush.Claim && every > 0 {
		wait = now.Truncate(every).Add(every).Sub(now)
	}

	ceiling := time.Duration(p.stream.Buffer.Flush.MaxAge)
	if ceiling <= 0 || b == nil || b.oldest.IsZero() {
		if wait < 0 {
			return 0
		}
		return wait
	}
	left := b.oldest.Add(ceiling).Sub(now)
	if left < wait {
		wait = left
	}
	if wait < 0 {
		return 0
	}
	return wait
}

// next is the earliest deadline across the buckets: ONE timer, not one per
// key. A bucket created now can only be later than one already waiting --
// the target is measured from each bucket's oldest event, and a new bucket's
// is the newest -- so the alarm the earliest bucket set is still the right
// one, and arm() can keep being a no-op while a timer runs.
func (p *pipe) next() time.Duration {
	d := time.Duration(-1)
	for _, b := range p.buckets {
		if w := p.deadline(b); d < 0 || w < d {
			d = w
		}
	}
	if d < 0 {
		return p.deadline(nil)
	}
	return d
}

// due says this bucket has waited what it was promised.
//
// Not `deadline(b) <= 0`: the two answer different questions. deadline says
// WHEN TO WAKE, and with a claim that is the window boundary -- at which
// instant it reads as a whole period away, so a dueness built on it would
// wake every window and flush nothing.
//
// With a claim the WINDOW is the unit. The claim buys one flush per window
// for the stream, and everything held goes in it, which is what it did
// before buckets existed. Without one, a bucket is due when it has waited
// `every` since its own oldest event -- the same thing the single batch's
// timer meant when it was armed once, on going from empty to held.
func (p *pipe) due(b *bucket) bool {
	if p.stream.Buffer.Flush.Claim {
		return true
	}
	if b == nil || b.oldest.IsZero() {
		return true
	}
	now := p.clock()
	every := p.stream.Buffer.Flush.Every
	if every <= 0 || !b.oldest.Add(every).After(now) {
		return true
	}
	// And the ceiling, which `deadline` also honours: a wake it brought
	// forward has to find something due, or the timer rings for nothing.
	ceiling := time.Duration(p.stream.Buffer.Flush.MaxAge)
	return ceiling > 0 && !b.oldest.Add(ceiling).After(now)
}

// clock is time.Now unless a test said otherwise.
func (p *pipe) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func newPipe(st Stream, hook Hook, sink, dead Sinker, maxBody int64, m *Metrics,
	meta Metastore,
) *pipe {
	p := &pipe{
		stream: st, hook: hook, sink: sink, dead: dead, maxBody: maxBody,
		queue: make(chan []sdk.Envelope, st.Buffer.Queue), metrics: m, meta: meta,
		buckets: map[string]*bucket{},
	}
	// Resolved once, here, where the sink arrives: a type assertion per event
	// to discover something that cannot change is work done 500 times a
	// second for an answer fixed at build time.
	//
	// In the constructor and not after it, because a pipe missing these is a
	// pipe whose buffer has no routing key -- one bucket where there should
	// be ninety-four -- and nothing about it looks wrong.
	p.admit, _ = sink.(Admitter)
	p.measure, _ = sink.(Measurer)
	p.wg.Add(st.Buffer.Workers)
	for range st.Buffer.Workers {
		go func() {
			defer p.wg.Done()
			// context.Background and not a request's: by the time a worker has
			// this batch the caller is gone, and a delivery cancelled because
			// one client disconnected would lose the events of every other
			// client in the same batch. send bounds itself with sendTimeout.
			for batch := range p.queue {
				_ = p.send(context.Background(), batch)
				// After send, not before: send buries what the sink refuses,
				// so a batch is accounted for either way once it returns.
				p.pending.Add(-int64(len(batch)))
			}
		}()
	}
	return p
}

// ServeHTTP accepts. It does the least work that is still honest and leaves
// the rest to the drain: a hook that takes two milliseconds must not be two
// milliseconds of the caller's p99.
func (p *pipe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, p.maxBody)
	events, err := decode(p.stream.Format, body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "the body is larger than this stream accepts", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(events) == 0 {
		http.Error(w, "the body carries no event", http.StatusBadRequest)
		return
	}

	got := p.prepare(events)
	accepted, rejected, archived := got.events, got.rejected, got.archived
	if len(accepted) > 0 {
		if err := p.enqueue(got); err != nil {
			p.metrics.count(p.metrics.saturated, 1, p.stream.Name)
			// 503 and not 202. The buffer is at its ceiling, so accepting
			// these would mean holding events with nowhere to put them --
			// and a 202 that ends in a silent drop is worse than a refusal.
			//
			// Safe to retry, in a way it is not for most services: the
			// ingestion_id is a frozen function of the event itself, so
			// sending this exact body again produces the same record rather
			// than a second one.
			w.Header().Set("Retry-After", "1")
			http.Error(w, "this stream's buffer is full and the sink is behind; "+
				"send the same body again -- it carries the same ingestion_id, "+
				"so a retry is the same record and not a duplicate",
				http.StatusServiceUnavailable)
			return
		}
	}

	p.metrics.count(p.metrics.received, int64(len(accepted)), p.stream.Name, p.stream.Format)

	// 202 and not 200: the gateway has ACCEPTED them, and with
	// `durability: memory` that is the whole of what it can honestly claim.
	// The tier that earns a 200 is the one that has written them down.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	answer := map[string]any{"accepted": len(accepted), "rejected": rejected}
	if archived > 0 {
		// Said out loud, because the alternative is what this looked like when
		// it was first run against a real payload: {"accepted":0,"rejected":null}
		// for an event that HAD been kept, whole, somewhere the caller was
		// never told about. A caller cannot tell that from "nothing happened",
		// and the difference is the whole point of the claim check.
		answer["archived"] = archived
	}
	_ = json.NewEncoder(w).Encode(answer)
}

// prepare runs the hook and builds the identity. A record that fails either is
// counted and skipped; the rest of the request still lands.
//
// The third return is how many were archived whole and dropped from the
// stream. Separate from `rejected` because it is not a refusal -- the event was
// kept -- and separate from `accepted` because it did not go into the stream.
// prepared is what survived, and where each survivor belongs.
//
// A struct and not five returns: `keys` and `rejected` are both []string and
// adjacent, which is the shape that gets swapped one day and compiles.
type prepared struct {
	events   []sdk.Envelope
	sizes    []int64
	keys     []string
	rejected []string
	archived int
}

func (p *pipe) prepare(events []arrival) prepared {
	out := make([]sdk.Envelope, 0, len(events))
	sizes := make([]int64, 0, len(events))

	// Only where there IS a key. A sink without a Measurer routes everywhere
	// the same, so a slice of empty strings would be an allocation and a
	// write per event to say nothing -- and most streams have one
	// destination. An empty `keys` means one bucket, which is what those
	// streams had before buckets existed.
	var keys []string
	if p.measure != nil {
		keys = make([]string, 0, len(events))
	}
	var rejected []string
	var archived int

	for i, a := range events {
		e := a.event
		if p.hook != nil {
			shaped, err := run(p.hook, e)
			if err != nil {
				rejected = append(rejected, fmt.Sprintf("event %d: %v", i, err))
				p.metrics.count(p.metrics.rejected, 1, p.stream.Name, ReasonHook)
				continue
			}
			if shaped == nil {
				// Dropped on purpose. Not a rejection: the hook decided.
				p.metrics.count(p.metrics.dropped, 1, p.stream.Name)
				continue
			}
			e = shaped
		}

		if p.big != nil {
			kept, err := p.archiveIfLarge(e)
			if err != nil {
				rejected = append(rejected, fmt.Sprintf("event %d: %v", i, err))
				p.metrics.count(p.metrics.rejected, 1, p.stream.Name, ReasonOversize)
				continue
			}
			if kept == nil {
				// Archived whole and dropped from the stream, on purpose: no
				// reduction hook was registered. Counted, because an event
				// that silently stops arriving is the worst outcome here.
				archived++
				continue
			}
			e = kept
		}

		// The event is final here -- the hook has run and the oversize path
		// has reduced it -- so this is where the arrival size is stamped. It
		// overwrites: a producer who sends this field must not be able to
		// report their own volume.
		// The key the volume is attributed to is also the key the buffer
		// groups by: the sink owns the routing either way, and asking twice
		// would be two answers to one question.
		key := ""
		if p.measure != nil {
			key = p.measure.Measure(e, a.bytes)
			p.metrics.ingested(p.stream.Name, key, a.bytes)
		}

		if p.admit != nil {
			if err := p.admit.Admit(e); err != nil {
				rejected = append(rejected, fmt.Sprintf("event %d: %v", i, err))
				p.metrics.count(p.metrics.rejected, 1, p.stream.Name, ReasonAdmit)
				continue
			}
		}

		env, reason, err := p.identify(e)
		if err != nil {
			rejected = append(rejected, fmt.Sprintf("event %d: %v", i, err))
			p.metrics.count(p.metrics.rejected, 1, p.stream.Name, reason)
			continue
		}
		out = append(out, env)
		sizes = append(sizes, int64(a.bytes))
		if keys != nil {
			keys = append(keys, key)
		}
	}
	return prepared{events: out, sizes: sizes, keys: keys, rejected: rejected, archived: archived}
}

// archiveIfLarge writes an oversized event somewhere whole and returns what
// should continue into the stream, or nil when nothing should.
//
// INLINE, on the request goroutine, and that is a deliberate exception to this
// package's own rule that a caller never waits for I/O. The reduction has to
// happen after the archive -- reducing first and archiving later means a
// failed archive leaves a reduced event pointing at an object that does not
// exist -- and the path is exceptional by construction: if it is hot, the
// limit is wrong.
func (p *pipe) archiveIfLarge(e map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("measuring the event: %w", err)
	}
	if len(encoded) <= p.big.limit {
		return e, nil
	}
	p.metrics.count(p.metrics.oversized, 1, p.stream.Name)

	// The whole event, before anything is taken out of it. An oversized
	// payload is usually the most interesting one somebody has.
	env := sdk.Envelope{Payload: e}
	ctx, cancel := context.WithTimeout(context.Background(), archiveTimeout)
	defer cancel()
	if _, err := p.big.archive.Write(ctx, []sdk.Envelope{env}); err != nil {
		return nil, fmt.Errorf("the event is %d bytes and the archive refused it: %w",
			len(encoded), err)
	}

	if p.big.reduce == nil {
		return nil, nil
	}
	reduced, err := run(p.big.reduce, e)
	if err != nil {
		return nil, fmt.Errorf("the event is %d bytes and the reduction hook "+
			"refused it: %w", len(encoded), err)
	}
	if reduced == nil {
		return nil, nil
	}

	// The claim check, stamped rather than left implicit: agreeing out of band
	// that the id is also the object's name works until somebody changes the
	// prefix, and then nothing says where to look.
	reduced[ColumnOversize] = true
	reduced[ColumnOversizeArchive] = p.big.archive.Describe()
	reduced[ColumnOversizeBytes] = len(encoded)
	return reduced, nil
}

// identify computes the ingestion_id and puts it on the payload.
//
// On the PAYLOAD and not only on the envelope, because what a subscriber
// receives is the payload: an id a consumer cannot see is an id that cannot
// deduplicate anything downstream.
//
// The second return is the metric's reason, and it is RETURNED rather than
// derived from the error text: a label matched against a message is a label
// that changes the day somebody improves the wording.
func (p *pipe) identify(e map[string]any) (sdk.Envelope, string, error) {
	id := p.stream.Identity

	key := Text(e[id.SourceKey])
	if key == "" {
		return sdk.Envelope{}, ReasonSourceKey, fmt.Errorf("field %q is missing or "+
			"empty, and it is this stream's source_key", id.SourceKey)
	}
	ts := Text(e[id.RecordTS])
	if ts == "" {
		return sdk.Envelope{}, ReasonRecordTS, fmt.Errorf("field %q is missing or "+
			"empty, and it is this stream's record_ts", id.RecordTS)
	}

	env := sdk.Envelope{
		Provider: id.Provider, Entity: id.Entity,
		SourceKey: key, RecordTS: ts,
		Payload: e,
	}

	// The envelope computes its own id, which keeps the frozen formula in the
	// one place that owns it. Reimplementing the concatenation here is exactly
	// how two implementations of one rule start to disagree.
	ingestionID, err := env.IngestionID()
	if err != nil {
		return sdk.Envelope{}, ReasonIdentity, err
	}

	// On the PAYLOAD too, because what a subscriber receives is the payload: an
	// id a consumer cannot see deduplicates nothing downstream.
	e[sdk.ColumnIngestionID] = ingestionID
	if p.stream.StampLoadedAt {
		// The SDK's column and the SDK's format, so a row this gateway lands
		// and a row a pipeline lands are the same shape.
		e[sdk.ColumnIngestionLoadedAt] = time.Now().UTC().Format(time.RFC3339)
	}
	return env, "", nil
}

// enqueue buffers, and hands a full batch to the pool. It returns errSaturated
// when the buffer is at its ceiling, and takes nothing in that case: the 503
// that follows has to be the truth about these events, not a note attached to
// events that were kept anyway.
//
// It never delivers. Handing a batch to the pool is a channel send, and when
// the pool is busy that send FAILS rather than waiting -- the batch stays
// buffered and leaves with the next request or the timer. The request goroutine
// leaves here in constant time either way.
func (p *pipe) enqueue(in prepared) error {
	envs, sizes, keys := in.events, in.sizes, in.keys
	var bytes int64
	for _, n := range sizes {
		bytes += n
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closing {
		// Shutting down. Refusing is the honest answer: the listener is on its
		// way out and an event accepted now has no drain left to leave by.
		return errSaturated
	}
	if p.held+len(envs) > p.stream.Buffer.MaxRecords {
		return errSaturated
	}
	// The other ceiling, and the one that actually stops an OOM. A count
	// cannot see bytes: 40,000 records is 80 MB of 2 KB events and 80 GB of
	// 2 MB ones, and the config had no way to say which this stream holds.
	if max := int64(p.stream.Buffer.MaxBytes); max > 0 && p.bytes+bytes > max {
		return errSaturated
	}
	p.gen++
	touched := make([]string, 0, 4)

	// last is the bucket the previous event went to. A request carries rows
	// for one table far more often than not -- and a sink with no routing key
	// carries "" for all of them -- so the common body costs ONE map lookup
	// rather than one per event.
	var last *bucket
	lastKey := ""
	haveLast := false

	for i, env := range envs {
		key := ""
		if keys != nil {
			key = keys[i]
		}
		b := last
		if !haveLast || key != lastKey {
			b = p.buckets[key]
			if b == nil {
				b = &bucket{}
				p.buckets[key] = b
				// The bucket goes from absent to held, so this is the moment
				// its wait starts counting from.
				b.oldest = p.clock()
			}
			last, lastKey, haveLast = b, key, true
		}
		if b.mark != p.gen {
			b.mark = p.gen
			touched = append(touched, key)
		}
		b.batch = append(b.batch, env)
		b.sizes = append(b.sizes, sizes[i])
		b.bytes += sizes[i]
	}
	p.held += len(envs)
	p.bytes += bytes

	// Sorted, so a request that fills two buckets hands them off in the same
	// order every time. Two runs that differ only in map order are two runs
	// nobody can compare.
	sort.Strings(touched)
	for _, key := range touched {
		b := p.buckets[key]
		// Whichever trigger is crossed first. Records before size only because
		// one of them has to be asked first; they are equals, and `trigger` on
		// the metric says which one fired so an operator can see it rather
		// than guess.
		switch {
		case len(b.batch) >= p.stream.Buffer.Flush.Records:
			p.handoff(key, TriggerRecords)
		case int64(p.stream.Buffer.Flush.Size) > 0 && b.bytes >= int64(p.stream.Buffer.Flush.Size):
			p.handoff(key, TriggerSize)
		}
	}
	// Whatever is still held goes anyway when it gets old, so a table at one
	// event a minute is not a table that never lands.
	p.arm()
	return nil
}

// handoff gives a full batch to a worker, or leaves it buffered. The caller
// holds the lock, and that is what makes it safe: the check against `closing`
// and the send on the queue are one step, so a timer firing while the gateway
// shuts down cannot send on a channel close has already closed.
//
// The send never waits. A full queue means every worker is busy, and the batch
// stays at the FRONT of the buffer -- the order events arrived in is the order
// they leave in, and a batch that lost a race should not also lose its place.
// The next request or the timer tries it again.
// handoff hands a full batch to the pool, and says what made it full.
//
// `why` is counted rather than logged, because the question it answers is
// operational and continuous: on a BigQuery stream a size flush firing
// routinely means the 60-second floor is being bypassed and the daily load-job
// quota is going with it. That is a number on a dashboard, not a line in a
// log.
func (p *pipe) handoff(key, why string) {
	if p.closing {
		return
	}
	b := p.take(key)
	if b == nil || len(b.batch) == 0 {
		return
	}
	select {
	case p.queue <- b.batch:
		p.pending.Add(int64(len(b.batch)))
		p.metrics.count(p.metrics.flushes, 1, p.stream.Name, why)
	default:
		// The pool is busy and the batch goes back, at the FRONT of ITS OWN
		// bucket: the order events arrived in is the order they leave in, per
		// key. Not counted as a flush -- nothing flushed, and counting here
		// would report one per attempt on a stream whose sink is behind.
		if cur := p.buckets[key]; cur != nil {
			cur.batch = append(b.batch, cur.batch...)
			cur.sizes = append(b.sizes, cur.sizes...)
			cur.bytes += b.bytes
			// What came back is older than anything that arrived meanwhile,
			// so the wait counts from when IT started. Losing this is how a
			// batch the pool keeps refusing resets its own clock forever.
			if cur.oldest.IsZero() || b.oldest.Before(cur.oldest) {
				cur.oldest = b.oldest
			}
		} else {
			p.buckets[key] = b
		}
		p.held += len(b.batch)
		p.bytes += b.bytes
		p.arm()
	}
}

// arm starts the flush timer if it is not already running. The caller holds
// the lock.
func (p *pipe) arm() {
	if p.timer != nil || p.closing || len(p.buckets) == 0 {
		return
	}
	d := p.next()
	// A floor, so a deadline that has already passed by a hair cannot become
	// a timer that re-arms at zero and spins.
	if d < time.Millisecond {
		d = time.Millisecond
	}
	p.timer = time.AfterFunc(d, p.flush)
}

// take empties the batch. The caller holds the lock.
// take empties the buffer, returning the batch and the sizes beside it.
//
// The sizes travel so handoff can put BOTH back when the pool is busy. Without
// them the byte ceiling would silently forget what it is already holding,
// which is the one ceiling whose job is to stop an OOM.
func (p *pipe) take(key string) *bucket {
	b := p.buckets[key]
	if b == nil {
		return nil
	}
	delete(p.buckets, key)
	p.held -= len(b.batch)
	p.bytes -= b.bytes
	// The timer is NOT stopped here. Other buckets are still waiting on it,
	// and the one that set it is the earliest -- taking a later one changes
	// nothing about when the alarm should ring.
	return b
}

// flush is the timer firing: a partial batch that has waited long enough. It
// goes out the same way a full one does.
func (p *pipe) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timer = nil
	if len(p.buckets) == 0 {
		return
	}
	// Only the buckets that have actually waited. The alarm was set by the
	// earliest one; the others are still inside their own target and go on
	// filling, which is the difference between a table's cadence and its
	// neighbour's.
	//
	// Sorted, so a window that takes several produces the same sequence every
	// time -- and so the claim asks for them in one order across replicas.
	due := make([]string, 0, len(p.buckets))
	for key, b := range p.buckets {
		if p.due(b) {
			due = append(due, key)
		}
	}
	sort.Strings(due)

	// Whoever lost keeps filling and comes back: arm() recomputes the
	// deadline against the ceiling, so a bucket that keeps losing gets a
	// shorter wait each time until max_age takes it.
	for _, key := range p.winners(due) {
		p.handoff(key, TriggerTime)
	}
	p.arm()
}

// winsTheWindow decides whether THIS replica flushes the current window.
//
// The window is `now` truncated to `every`, so every replica computes the same
// one without a shared clock -- a wall-clock boundary rather than "one minute
// since I started". The claim's TTL is `every` for the same reason: exactly one
// flush per window, and a winner that dies before delivering costs that window
// and no more.
//
// Three ways to win without asking:
//
//   - the claim is off, which is the default and today's behaviour;
//   - there is no metastore, so there is nobody to ask;
//   - the CEILING has closed. `max_age` is a promise and a promise that can be
//     deferred is not one. deadline() returning zero means the oldest event has
//     waited its full ceiling, and at that point this batch leaves whatever
//     anybody else is doing.
//
// And one way to win by failing: an unreachable metastore behaves as if the
// claim were ours, which is the direction claimDDL already chose. Losing
// coordination costs load jobs; refusing to flush holds data, and the buffer
// exists to not do that.
func (p *pipe) winners(due []string) []string {
	if !p.stream.Buffer.Flush.Claim || p.meta == nil || len(due) == 0 {
		return due
	}

	every := p.stream.Buffer.Flush.Every
	window := p.clock().UTC().Truncate(every).Format(time.RFC3339)

	won := make([]string, 0, len(due))
	ask := make([]string, 0, len(due))
	keys := make([]string, 0, len(due))
	for _, key := range due {
		// The ceiling has closed for this bucket, so it leaves whatever
		// anybody else is doing. `max_age` is a promise, and a promise that
		// can be deferred is not one.
		if p.deadline(p.buckets[key]) <= 0 {
			p.metrics.count(p.metrics.windows, 1, p.stream.Name, CeilingWindow)
			won = append(won, key)
			continue
		}
		ask = append(ask, key)
		keys = append(keys, p.claimKey(key, window))
	}
	if len(ask) == 0 {
		return won
	}

	// ONE call for all of them where the backend can. Slice 0 measured 94
	// sequential claims at 2.589s of held mutex against a Redis at 25ms RTT,
	// and admission's p99 tracked that 1:1 -- `enqueue` takes `p.mu`
	// unconditionally, so every request arriving in the window pays the whole
	// of it. Pipelined the same 94 cost 29.8ms, which is what ONE claim costs
	// today.
	ctx, cancel := context.WithTimeout(context.Background(), claimTimeout)
	defer cancel()
	got, err := claimAll(ctx, p.meta, keys, every)
	if err != nil {
		// Coordination is off and every flush still looks like a flush, so
		// this counter is the only thing that says so. Failing OPEN, per key,
		// for the reason it always did: losing coordination costs load jobs,
		// and refusing to flush holds events already answered 202.
		for range ask {
			p.metrics.count(p.metrics.windows, 1, p.stream.Name, UnreachableWindow)
		}
		return append(won, ask...)
	}
	for i, key := range ask {
		outcome := YieldedWindow
		if got[i] {
			outcome = WonWindow
			won = append(won, key)
		}
		p.metrics.count(p.metrics.windows, 1, p.stream.Name, outcome)
	}
	return won
}

// claimKey names the window ONE bucket is contesting.
//
// The routing key is in it, because without it two tables on one stream
// contest the same window: whichever reaches the boundary first takes it and
// the other waits, so one table's cadence is another's -- which is issue #38
// again, one level below where it was found. Two replicas behind a load
// balancer do not hold the same tables, and with N replicas a table can wait
// N windows for a claim it never wanted.
//
// A stream with no routing key keeps the key it had, exactly: a deployment
// upgrading into this must not find its replicas contesting a window that
// moved.
func (p *pipe) claimKey(key, window string) string {
	base := "brevis:gw:" + p.gateway + ":" + p.stream.Name
	if key != "" {
		base += ":" + key
	}
	return base + ":flush:" + window
}

// BulkClaimer is a metastore that can claim several keys in ONE round trip.
//
// Optional, like Admitter and Measurer, and satisfied by structural typing: a
// store without it is asked one key at a time, which is what every store did
// before the trigger had keys.
//
// It is not a convenience. The claim runs under `p.mu`, so its duration is
// admission's tail for every request that arrives during it -- and the loop
// below is the difference between 29.8ms and 2.589s on a Redis at 25ms RTT.
type BulkClaimer interface {
	ClaimMany(ctx context.Context, keys []string, ttl time.Duration) ([]bool, error)
}

func claimAll(ctx context.Context, m Metastore, keys []string, ttl time.Duration) ([]bool, error) {
	if bulk, ok := m.(BulkClaimer); ok {
		got, err := bulk.ClaimMany(ctx, keys, ttl)
		if err != nil {
			return nil, err
		}
		if len(got) != len(keys) {
			return nil, fmt.Errorf("the metastore answered %d claims for %d keys",
				len(got), len(keys))
		}
		return got, nil
	}
	out := make([]bool, len(keys))
	for i, key := range keys {
		got, err := m.Claim(ctx, key, ttl)
		if err != nil {
			return nil, err
		}
		out[i] = got
	}
	return out, nil
}

// claimTimeout bounds the one call the timer path makes to the metastore.
//
// Short, because this runs while the pipe's lock is held: a backend that hangs
// would stop every request for this stream, which is a far worse outcome than
// the duplicate load job the claim exists to avoid.
const claimTimeout = 500 * time.Millisecond

// send delivers a batch, retrying, and gives up into the dead letter. It
// returns what the sink last said, which is nil when the batch landed and the
// cause when it was buried.
//
// The ctx is never a request's. The caller that filled a batch is gone by the
// time it is delivered, and a batch cancelled because one client disconnected
// would lose the events of every OTHER client in it. On shutdown it is the
// drain's deadline, which is a window the operator set.
func (p *pipe) send(ctx context.Context, batch []sdk.Envelope) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	r := p.stream.Retry
	name, sink := p.stream.Name, p.sink.Describe()
	p.metrics.observe(p.metrics.batchSize, float64(len(batch)), name)
	start := time.Now()

	var err error
	for attempt := 1; attempt <= r.Attempts; attempt++ {
		var n int64
		n, err = p.sink.Write(ctx, batch)
		if err == nil {
			// The duration covers the retries, because that is what the batch
			// actually cost: a p99 that reported only the successful attempt
			// would look healthy through an outage.
			p.metrics.observe(p.metrics.delivery, time.Since(start).Seconds(), name, sink)
			p.metrics.count(p.metrics.batches, 1, name, sink, OutcomeDelivered)
			slog.Info("delivered", "stream", name,
				"sink", sink, "events", n, "attempt", attempt)
			return nil
		}
		p.metrics.count(p.metrics.batches, 1, name, sink, OutcomeRetried)
		if attempt == r.Attempts {
			break
		}
		wait := backoff(r, attempt)
		slog.Warn("the sink refused a batch; retrying",
			"stream", p.stream.Name, "sink", p.sink.Describe(),
			"attempt", attempt, "of", r.Attempts, "in", wait, "error", err)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			err = fmt.Errorf("%w (and the send window closed)", err)
			goto giveUp
		}
	}

giveUp:
	p.metrics.observe(p.metrics.delivery, time.Since(start).Seconds(), name, sink)
	p.metrics.count(p.metrics.batches, 1, name, sink, OutcomeBuried)
	p.bury(ctx, batch, err)
	return err
}

// bury writes what the sink would not take, with the reason attached.
//
// The reason travels ON the record and not only in a log, because whoever finds
// this file later has the events and not the log -- and "why is this here" is
// the first thing they will ask.
//
// A failure HERE is the end of the line, and it says so at ERROR with the count:
// there is nowhere further to put them, and a silent loss at the last step would
// be the exact failure the dead letter exists to prevent.
func (p *pipe) bury(ctx context.Context, batch []sdk.Envelope, cause error) {
	reason := cause.Error()
	buried := make([]sdk.Envelope, 0, len(batch))
	for _, e := range batch {
		row, ok := e.Payload.(map[string]any)
		if !ok {
			buried = append(buried, e)
			continue
		}
		// A copy: the batch may still be referenced, and stamping the original
		// would put the failure of one sink into a record another one holds.
		out := make(map[string]any, len(row)+3)
		for k, v := range row {
			out[k] = v
		}
		out["_dead_letter_reason"] = reason
		out["_dead_letter_sink"] = p.sink.Describe()
		out["_dead_letter_at"] = time.Now().UTC().Format(time.RFC3339)
		e.Payload = out
		buried = append(buried, e)
	}

	n, err := p.dead.Write(ctx, buried)
	if err == nil {
		p.metrics.count(p.metrics.buried, n, p.stream.Name, p.sink.Describe())
	}
	if err != nil {
		slog.Error("the dead letter refused them too, and they are lost",
			"stream", p.stream.Name, "events", len(batch),
			"sink", p.sink.Describe(), "dead_letter", p.dead.Describe(),
			"cause", reason, "error", err)
		return
	}
	slog.Error("a batch went to the dead letter",
		"stream", p.stream.Name, "events", n,
		"sink", p.sink.Describe(), "dead_letter", p.dead.Describe(), "cause", reason)
}

// backoff doubles from Retry.Backoff, capped, with jitter.
//
// Jitter because N replicas losing the same sink retry on the same schedule
// otherwise, and a sink coming back up meets every one of them at once -- which
// is how a recovery becomes a second outage.
func backoff(r Retry, attempt int) time.Duration {
	wait := r.Backoff << (attempt - 1)
	if wait > r.MaxBackoff || wait <= 0 {
		wait = r.MaxBackoff
	}
	//nolint:gosec // jitter, not a secret
	return wait/2 + time.Duration(rand.Int64N(int64(wait/2)+1))
}

// close drains what is buffered THROUGH the same path a full batch takes:
// retried, and buried with the reason when the sink will not have it.
//
// Writing straight to the sink here was a hole. A batch the sink refused at
// shutdown was lost -- no retry, no dead letter -- which is the one outcome the
// dead letter exists to prevent, and it happened on the ordinary path of every
// deploy.
//
// The order is: stop accepting, put what is left on the queue, close the queue,
// wait for the workers. Closing the queue is what ends them, and waiting is
// what makes a clean stop mean the events left.
func (p *pipe) close(ctx context.Context) error {
	p.mu.Lock()
	p.closing = true
	// The timer is stopped below, so no flush is armed past this point -- and
	// one already running is blocked on this lock and will find closing set.
	//
	// The sizes go nowhere: the buffer is finished, and nothing will be
	// admitted against the ceiling again.
	// One batch of everything, sorted by key so a drain is reproducible. The
	// sink re-groups it -- `router.Write` does exactly that -- and shutdown
	// is about not losing events, not about cadence: splitting it here would
	// buy nothing and cost a load job per bucket on the way out.
	keys := make([]string, 0, len(p.buckets))
	for key := range p.buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var ready []sdk.Envelope
	for _, key := range keys {
		if b := p.take(key); b != nil {
			ready = append(ready, b.batch...)
		}
	}
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()

	// Nothing else can send on the queue now: closing is set, and every send
	// happens under the same lock behind that check. So this is the last
	// batch on, and closing the channel after it is what ends the workers.
	if len(ready) > 0 {
		select {
		case p.queue <- ready:
			p.pending.Add(int64(len(ready)))
		case <-ctx.Done():
			// No room before the window closed. Delivered here rather than
			// dropped: send buries what the sink refuses, and the alternative
			// is losing a batch on the way out.
			_ = p.send(context.Background(), ready)
		}
	}
	close(p.queue)

	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// The workers are still delivering, and the process is about to
		// return: what is still pending is what this stop loses.
		//
		// Reported rather than waited on -- one batch can hold the full retry
		// window -- and reported WITH THE COUNT, because the deadline alone
		// does not say whether four events were lost or forty thousand. The
		// caller turns this into a non-zero exit, which is the only thing a
		// supervisor can see.
		return fmt.Errorf("stream %q: the drain did not finish and %d accepted "+
			"event(s) were not delivered: %w",
			p.stream.Name, p.pending.Load(), ctx.Err())
	}
}

// arrival is one decoded event and the number of bytes it arrived as.
//
// The size is the RECEIVED encoding, measured here where it is free, and that
// is the honest name for it. It is not what the event weighs in memory -- a
// map[string]any is several times its JSON -- and it is not what a hook may
// turn it into. It is a proxy, and it is the right one: it costs nothing, it
// moves with the payload, and the thing it protects against is a producer
// whose records got ten times larger.
//
// Exact would mean a json.Marshal per event, which today only happens when
// `oversize` is configured. At the rates this buffer sustains that is real
// CPU spent to refine a number that is already an approximation of memory.
type arrival struct {
	event map[string]any
	bytes int
}

// decode reads a body in the format the stream declared. Never sniffed:
// guessing is how a batch of a thousand becomes one row holding an array.
func decode(format string, r io.Reader) ([]arrival, error) {
	switch format {
	case FormatJSON:
		// Read whole rather than streamed, because one body is one event and
		// its length is the measurement. It is already bounded: the handler
		// wraps the body in a MaxBytesReader at `listen.max_body`.
		body, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		var one map[string]any
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, fmt.Errorf("the body is not one JSON object: %w", err)
		}
		return []arrival{{event: one, bytes: len(body)}}, nil

	case FormatArray:
		// Through RawMessage, so each element's bytes are known without a
		// second pass. Unmarshalling straight into []map[string]any would
		// lose every element's extent, and re-encoding to recover it would
		// cost a marshal per event to learn what the decoder already had.
		var raw []json.RawMessage
		if err := json.NewDecoder(r).Decode(&raw); err != nil {
			return nil, fmt.Errorf("the body is not a JSON array of objects: %w", err)
		}
		out := make([]arrival, 0, len(raw))
		for i, m := range raw {
			var one map[string]any
			if err := json.Unmarshal(m, &one); err != nil {
				return nil, fmt.Errorf("element %d is not a JSON object: %w", i, err)
			}
			out = append(out, arrival{event: one, bytes: len(m)})
		}
		return out, nil

	case FormatNDJSON:
		var out []arrival
		sc := bufio.NewScanner(r)
		// The same ceiling the engine's executors use, and for the same reason:
		// a Scanner that overflows stops reading in SILENCE, which here would
		// be a request that reported success having read half of it.
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for n := 1; sc.Scan(); n++ {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var one map[string]any
			if err := json.Unmarshal([]byte(line), &one); err != nil {
				return nil, fmt.Errorf("line %d is not a JSON object: %w", n, err)
			}
			out = append(out, arrival{event: one, bytes: len(line)})
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}
