package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// Server is the gateway listening.
type Server struct {
	cfg   *Config
	mux   *http.ServeMux
	pipes []*pipe
}

// New wires a config to the hooks this binary carries.
//
// Everything that can fail does so HERE and not on a request: an unknown hook,
// an unknown sink, a bad path. A gateway that starts on a file it half
// understood drops events for a reason nobody can see.
func New(cfg *Config, hooks *Hooks) (*Server, error) {
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

		sink, err := build(st.Sink)
		if err != nil {
			return nil, fmt.Errorf("stream %q: %w", st.Name, err)
		}

		p := newPipe(st, hook, sink, int64(cfg.Listen.MaxBody))
		s.pipes = append(s.pipes, p)
		s.mux.Handle("POST "+st.Path, p)
	}

	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

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
	maxBody int64

	mu      sync.Mutex
	batch   []sdk.Envelope
	timer   *time.Timer
	closing bool
}

func newPipe(st Stream, hook Hook, sink Sinker, maxBody int64) *pipe {
	return &pipe{stream: st, hook: hook, sink: sink, maxBody: maxBody}
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

// send hands the batch to the driver.
//
// context.Background and not the request's: the client is gone by now, and a
// batch cancelled because one caller disconnected would lose the events of
// every other caller in it.
func (p *pipe) send(_ context.Context, batch []sdk.Envelope) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	n, err := p.sink.Write(ctx, batch)
	if err != nil {
		slog.Error("the sink refused a batch",
			"stream", p.stream.Name, "sink", p.sink.Describe(),
			"delivered", n, "of", len(batch), "error", err)
		return
	}
	slog.Info("delivered", "stream", p.stream.Name, "sink", p.sink.Describe(), "events", n)
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
