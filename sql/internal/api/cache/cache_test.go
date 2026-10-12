package cache

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// memory is the KV in a map, and can be told to fail.
type memory struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls map[string]time.Duration
	down bool
}

func newMemory() *memory { return &memory{data: map[string][]byte{}, ttls: map[string]time.Duration{}} }

var errDown = errors.New("redis could not be reached")

func (m *memory) Get(_ context.Context, k string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, false, errDown
	}
	v, ok := m.data[k]
	return v, ok, nil
}
func (m *memory) Set(_ context.Context, k string, v []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return errDown
	}
	m.data[k], m.ttls[k] = v, ttl
	return nil
}
func (m *memory) MGet(_ context.Context, keys ...string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, errDown
	}
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = m.data[k]
	}
	return out, nil
}

// THE KEY IS EVERYTHING THE ANSWER DEPENDS ON: the endpoint, the tenant,
// the statement, its arguments, and each model's generation. Change any one
// and it is a different key; change none and it is the same, every time.
func TestTheKeyChangesWithEverythingTheAnswerDependsOn(t *testing.T) {
	args := []dialect.Arg{{Name: "c", Value: "x"}}
	base := Key("api", "conn", "clicks", "acme", "select $1", args, []string{"g1"})
	if base != Key("api", "conn", "clicks", "acme", "select $1", args, []string{"g1"}) {
		t.Error("the same inputs gave two keys")
	}
	for name, other := range map[string]string{
		"endpoint":      Key("api", "conn", "orders", "acme", "select $1", args, []string{"g1"}),
		"tenant":        Key("api", "conn", "clicks", "globex", "select $1", args, []string{"g1"}),
		"sql":           Key("api", "conn", "clicks", "acme", "select $2", args, []string{"g1"}),
		"args":          Key("api", "conn", "clicks", "acme", "select $1", []dialect.Arg{{Name: "c", Value: "y"}}, []string{"g1"}),
		"arg type":      Key("api", "conn", "clicks", "acme", "select $1", []dialect.Arg{{Name: "c", Value: int64(1)}}, []string{"g1"}),
		"generation":    Key("api", "conn", "clicks", "acme", "select $1", args, []string{"g2"}),
		"no generation": Key("api", "conn", "clicks", "acme", "select $1", args, []string{""}),
		"api":           Key("other", "conn", "clicks", "acme", "select $1", args, []string{"g1"}),
		"connection":    Key("api", "other", "clicks", "acme", "select $1", args, []string{"g1"}),
	} {
		if other == base {
			t.Errorf("a different %s gave the same key", name)
		}
	}
	// SEPARATORS CANNOT BE FORGED: a tenant ending in what the endpoint
	// starts with is not the same pair.
	if Key("api", "conn", "ab", "c", "q", nil, nil) == Key("api", "conn", "a", "bc", "q", nil, nil) {
		t.Error("two pairs that concatenate alike share a key")
	}
	if !strings.HasPrefix(base, "brevis:api:{brevis}:api:clicks:") {
		t.Errorf("key %q", base)
	}
}

func TestAnAnswerRoundTripsCompressed(t *testing.T) {
	kv := newMemory()
	s := New(kv, time.Hour, 1<<20)
	body := []byte(`{"data":[` + strings.Repeat(`{"day":"2026-10-05","clicks":412},`, 200) + `{}]}`)
	stored, err := s.Put(context.Background(), "k", body)
	if err != nil || !stored {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
	if len(kv.data["k"]) >= len(body)/4 {
		t.Errorf("%d bytes stored for a %d-byte answer: it is not compressed", len(kv.data["k"]), len(body))
	}
	if kv.ttls["k"] != time.Hour {
		t.Errorf("stored with ttl %s", kv.ttls["k"])
	}
	compressed, found, err := s.Get(context.Background(), "k")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	got, err := Decompress(compressed)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("the answer came back different: %v", err)
	}
}

// LARGER THAN max_entry IS SERVED AND NOT KEPT: one huge answer evicting a
// thousand small ones is the cache working against itself.
func TestAnAnswerOverMaxEntryIsNotStored(t *testing.T) {
	kv := newMemory()
	s := New(kv, time.Hour, 64)
	noise := make([]byte, 4096)
	for i := range noise {
		noise[i] = byte(i*7919 + i/3)
	}
	stored, err := s.Put(context.Background(), "k", noise)
	if err != nil || stored || len(kv.data) != 0 {
		t.Errorf("stored=%v err=%v", stored, err)
	}
}

func TestGenerationsAreReadInOneRoundTrip(t *testing.T) {
	kv := newMemory()
	kv.data["brevis:gen:{brevis}:marts.a"] = []byte("g-a")
	got, err := New(kv, time.Hour, 1<<20).Generations(context.Background(), []string{"marts.a", "marts.b"})
	if err != nil || strings.Join(got, ",") != "g-a," {
		t.Errorf("%q %v", got, err)
	}
}

// CONCURRENT MISSES ON ONE KEY ARE ONE QUERY. A rebuild invalidates every
// answer at once, and a hundred dashboards refreshing must not become a
// hundred warehouse queries for the same bytes.
func TestConcurrentMissesOnOneKeyRunOnce(t *testing.T) {
	var g Group[[]byte]
	var runs atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, _ := g.Do("k", func() ([]byte, error) {
				runs.Add(1)
				<-release
				return []byte("answer"), nil
			})
			results[i] = v
		}(i)
	}
	// EVERY WAITER IS INSIDE Do BEFORE THE RUN ENDS. A sleep here was a
	// guess: on a slow runner a goroutine still on its way would arrive
	// after the run and start a second one, failing a test of a property
	// that holds.
	for {
		g.mu.Lock()
		c := g.calls["k"]
		arrived := c != nil && c.dups == 99
		g.mu.Unlock()
		if arrived {
			break
		}
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Errorf("%d runs for one key", n)
	}
	for _, r := range results {
		if string(r) != "answer" {
			t.Fatalf("a waiter got %q", r)
		}
	}
}
