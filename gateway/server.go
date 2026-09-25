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

		p := newPipe(st, hook, sink, dead, int64(cfg.Listen.MaxBody), s.metrics)
		// Resolved once, at startup: a type assertion per event to discover
		// something that cannot change is work done 500 times a second for an
		// answer fixed at build time.
		p.admit, _ = sink.(Admitter)
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
			buffered := len(p.batch)
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
	big     *oversize
	admit   Admitter

	mu      sync.Mutex
	batch   []sdk.Envelope
	timer   *time.Timer
	closing bool
}

func newPipe(st Stream, hook Hook, sink, dead Sinker, maxBody int64, m *Metrics) *pipe {
	p := &pipe{
		stream: st, hook: hook, sink: sink, dead: dead, maxBody: maxBody,
		queue: make(chan []sdk.Envelope, st.Buffer.Queue), metrics: m,
	}
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

	accepted, rejected, archived := p.prepare(events)
	if len(accepted) > 0 {
		if err := p.enqueue(accepted); err != nil {
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
func (p *pipe) prepare(events []map[string]any) ([]sdk.Envelope, []string, int) {
	out := make([]sdk.Envelope, 0, len(events))
	var rejected []string
	var archived int

	for i, e := range events {
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
	}
	return out, rejected, archived
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
func (p *pipe) enqueue(envs []sdk.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closing {
		// Shutting down. Refusing is the honest answer: the listener is on its
		// way out and an event accepted now has no drain left to leave by.
		return errSaturated
	}
	if len(p.batch)+len(envs) > p.stream.Buffer.MaxRecords {
		return errSaturated
	}
	p.batch = append(p.batch, envs...)

	if len(p.batch) >= p.stream.Buffer.Flush.Records {
		p.handoff()
		return nil
	}
	// A partial batch goes anyway when it gets old, so a stream at one event a
	// minute is not a stream that never lands.
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
func (p *pipe) handoff() {
	if p.closing {
		return
	}
	ready := p.take()
	select {
	case p.queue <- ready:
		p.pending.Add(int64(len(ready)))
	default:
		p.batch = append(ready, p.batch...)
		p.arm()
	}
}

// arm starts the flush timer if it is not already running. The caller holds
// the lock.
func (p *pipe) arm() {
	if p.timer == nil && !p.closing {
		p.timer = time.AfterFunc(p.stream.Buffer.Flush.Every, p.flush)
	}
}

// take empties the batch. The caller holds the lock.
func (p *pipe) take() []sdk.Envelope {
	ready := p.batch
	p.batch = nil
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	return ready
}

// flush is the timer firing: a partial batch that has waited long enough. It
// goes out the same way a full one does.
func (p *pipe) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timer = nil
	if len(p.batch) > 0 {
		p.handoff()
	}
}

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
	// take stops the timer, so no flush is armed past this point -- and one
	// already running is blocked on this lock and will find closing set.
	ready := p.take()
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

// decode reads a body in the format the stream declared. Never sniffed:
// guessing is how a batch of a thousand becomes one row holding an array.
func decode(format string, r io.Reader) ([]map[string]any, error) {
	switch format {
	case FormatJSON:
		var one map[string]any
		if err := json.NewDecoder(r).Decode(&one); err != nil {
			return nil, fmt.Errorf("the body is not one JSON object: %w", err)
		}
		return []map[string]any{one}, nil

	case FormatArray:
		var many []map[string]any
		if err := json.NewDecoder(r).Decode(&many); err != nil {
			return nil, fmt.Errorf("the body is not a JSON array of objects: %w", err)
		}
		return many, nil

	case FormatNDJSON:
		var out []map[string]any
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
			out = append(out, one)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}
