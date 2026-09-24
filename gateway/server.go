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
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// sendTimeout bounds one delivery, retries included. Past it the batch goes to
// the dead letter: a send that has been trying for a minute is a sink that is
// down, and holding the events in memory while it is down is how memory becomes
// the outage.
const sendTimeout = 60 * time.Second

// Server is the gateway listening.
type Server struct {
	cfg   *Config
	mux   *http.ServeMux
	pipes []*pipe
	keys  []string
}

// Option adjusts a Server at construction.
type Option func(*options)

type options struct{ sinks map[string]Sinker }

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
	s := &Server{cfg: cfg, mux: http.NewServeMux()}

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
			if sink, err = build(st.Sink); err != nil {
				return nil, fmt.Errorf("stream %q: %w", st.Name, err)
			}
		}
		dead, err := build(st.DeadLetter)
		if err != nil {
			return nil, fmt.Errorf("stream %q: dead_letter: %w", st.Name, err)
		}

		p := newPipe(st, hook, sink, dead, int64(cfg.Listen.MaxBody))
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
	return s, nil
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
	var first error
	for _, p := range s.pipes {
		if err := p.close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// pipe is one stream: the handler, its buffer and its sink.
type pipe struct {
	stream  Stream
	hook    Hook
	sink    Sinker
	dead    Sinker
	maxBody int64

	mu      sync.Mutex
	batch   []sdk.Envelope
	timer   *time.Timer
	closing bool
}

func newPipe(st Stream, hook Hook, sink, dead Sinker, maxBody int64) *pipe {
	return &pipe{stream: st, hook: hook, sink: sink, dead: dead, maxBody: maxBody}
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

	accepted, rejected := p.prepare(events)
	if len(accepted) > 0 {
		p.enqueue(r.Context(), accepted)
	}

	// 202 and not 200: the gateway has ACCEPTED them, and with
	// `durability: memory` that is the whole of what it can honestly claim.
	// The tier that earns a 200 is the one that has written them down.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted": len(accepted),
		"rejected": rejected,
	})
}

// prepare runs the hook and builds the identity. A record that fails either is
// counted and skipped; the rest of the request still lands.
func (p *pipe) prepare(events []map[string]any) ([]sdk.Envelope, []string) {
	out := make([]sdk.Envelope, 0, len(events))
	var rejected []string

	for i, e := range events {
		if p.hook != nil {
			shaped, err := run(p.hook, e)
			if err != nil {
				rejected = append(rejected, fmt.Sprintf("event %d: %v", i, err))
				continue
			}
			if shaped == nil {
				// Dropped on purpose. Not a rejection: the hook decided.
				continue
			}
			e = shaped
		}

		env, err := p.identify(e)
		if err != nil {
			rejected = append(rejected, fmt.Sprintf("event %d: %v", i, err))
			continue
		}
		out = append(out, env)
	}
	return out, rejected
}

// identify computes the ingestion_id and puts it on the payload.
//
// On the PAYLOAD and not only on the envelope, because what a subscriber
// receives is the payload: an id a consumer cannot see is an id that cannot
// deduplicate anything downstream.
func (p *pipe) identify(e map[string]any) (sdk.Envelope, error) {
	id := p.stream.Identity

	key := text(e[id.SourceKey])
	if key == "" {
		return sdk.Envelope{}, fmt.Errorf("field %q is missing or empty, and it is "+
			"this stream's source_key", id.SourceKey)
	}
	ts := text(e[id.RecordTS])
	if ts == "" {
		return sdk.Envelope{}, fmt.Errorf("field %q is missing or empty, and it is "+
			"this stream's record_ts", id.RecordTS)
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
		return sdk.Envelope{}, err
	}

	// On the PAYLOAD too, because what a subscriber receives is the payload: an
	// id a consumer cannot see deduplicates nothing downstream.
	e[sdk.ColumnIngestionID] = ingestionID
	return env, nil
}

// enqueue adds to the batch and sends when it is full, or arms the timer that
// sends it when it is old.
func (p *pipe) enqueue(ctx context.Context, envs []sdk.Envelope) {
	p.mu.Lock()
	p.batch = append(p.batch, envs...)

	if len(p.batch) >= p.stream.Buffer.Flush.Records {
		ready := p.take()
		p.mu.Unlock()
		p.send(ctx, ready)
		return
	}

	// A partial batch goes anyway when it gets old, so a stream at one event a
	// minute is not a stream that never lands.
	if p.timer == nil && !p.closing {
		p.timer = time.AfterFunc(p.stream.Buffer.Flush.Every, p.flush)
	}
	p.mu.Unlock()
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

func (p *pipe) flush() {
	p.mu.Lock()
	ready := p.take()
	p.mu.Unlock()
	if len(ready) > 0 {
		p.send(context.Background(), ready)
	}
}

// send delivers a batch, retrying, and gives up into the dead letter.
//
// context.Background and not the request's: the client is gone by now, and a
// batch cancelled because one caller disconnected would lose the events of
// every other caller in it.
func (p *pipe) send(_ context.Context, batch []sdk.Envelope) {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	r := p.stream.Retry
	var err error
	for attempt := 1; attempt <= r.Attempts; attempt++ {
		var n int64
		n, err = p.sink.Write(ctx, batch)
		if err == nil {
			slog.Info("delivered", "stream", p.stream.Name,
				"sink", p.sink.Describe(), "events", n, "attempt", attempt)
			return
		}
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
	p.bury(ctx, batch, err)
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

func (p *pipe) close(ctx context.Context) error {
	p.mu.Lock()
	p.closing = true
	ready := p.take()
	p.mu.Unlock()
	if len(ready) == 0 {
		return nil
	}
	_, err := p.sink.Write(ctx, ready)
	return err
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
