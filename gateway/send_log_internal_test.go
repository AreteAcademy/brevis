package gateway

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// syncBuffer is a log sink the send loop and the test can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func captureWarnings(t *testing.T) *syncBuffer {
	t.Helper()
	var b syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &b
}

// Issue #85: one attempt spent the whole send window, and the log still said
// "retrying attempt=1 of=4" 330 ms before the batch was buried. Attempts 2-4
// never ran. A line promising a retry that cannot happen sends whoever reads
// it looking for the retries.
func TestASpentWindowDoesNotPromiseARetry(t *testing.T) {
	restore := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = restore })
	logs := captureWarnings(t)

	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 4, Backoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}},
		sink:    blockingSink{},
		dead:    &recordingDead{},
		metrics: NewMetrics(),
	}
	_ = p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})

	out := logs.String()
	if strings.Contains(out, "retrying") {
		t.Errorf("the log promised a retry the window had no room for:\n%s", out)
	}
	if !strings.Contains(out, "send window is exhausted") {
		t.Errorf("the log does not say the window ran out:\n%s", out)
	}
}

// failsOnce refuses the first write and takes the second.
type failsOnce struct {
	mu    sync.Mutex
	calls int
}

func (*failsOnce) Describe() string { return "fails-once" }
func (f *failsOnce) Write(_ context.Context, b []sdk.Envelope) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return 0, errors.New("transient")
	}
	return int64(len(b)), nil
}

// The other side: with room left, the retry is announced and then happens.
func TestARetryWithRoomIsAnnounced(t *testing.T) {
	logs := captureWarnings(t)
	sink := &failsOnce{}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 2, Backoff: time.Millisecond, MaxBackoff: time.Millisecond}},
		sink:    sink,
		dead:    &recordingDead{},
		metrics: NewMetrics(),
	}
	if err := p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}}); err != nil {
		t.Fatalf("the second attempt should have landed: %v", err)
	}
	if !strings.Contains(logs.String(), "retrying") {
		t.Errorf("a retry ran without being announced:\n%s", logs.String())
	}
}

// keySink records the batch key each attempt was handed, and refuses until the
// last attempt.
type keySink struct {
	mu   sync.Mutex
	keys []string
	fail int
}

func (*keySink) Describe() string { return "keys" }
func (k *keySink) Write(ctx context.Context, b []sdk.Envelope) (int64, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys = append(k.keys, BatchKeyOf(ctx))
	if len(k.keys) <= k.fail {
		return 0, errors.New("transient")
	}
	return int64(len(b)), nil
}

// Issue #85: the retry must be recognisable as the SAME write, or BigQuery
// starts a second job for it. One key per batch, the same on every attempt.
func TestEveryAttemptOfABatchCarriesOneKey(t *testing.T) {
	sink := &keySink{fail: 2}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 3, Backoff: time.Millisecond, MaxBackoff: time.Millisecond}},
		sink:    sink,
		dead:    &recordingDead{},
		metrics: NewMetrics(),
	}
	batch := []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}}
	if err := p.send(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if len(sink.keys) != 3 || sink.keys[0] == "" || sink.keys[0] != sink.keys[1] || sink.keys[1] != sink.keys[2] {
		t.Fatalf("attempts carried keys %q: one batch, one non-empty key", sink.keys)
	}

	// And two batches are two writes, however alike.
	if err := p.send(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if sink.keys[3] == sink.keys[0] {
		t.Errorf("two batches share the key %q: the second would be taken for the first", sink.keys[0])
	}
}
