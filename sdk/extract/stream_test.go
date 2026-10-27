package extract

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// A Stream reads the body itself, so DataKey would sit there unwrapping
// nothing. It is refused before a single request goes out, as it is with
// Records (#140).
func TestAStreamWithADataKeyIsRefusedBeforeAnyRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := Stream(context.Background(), core.Source{URL: srv.URL, DataKey: "data"},
		func(core.Response, func(any) error) error { return nil })
	if err == nil {
		t.Fatal("a Stream with a DataKey was accepted; DataKey would be ignored")
	}
	if !strings.Contains(err.Error(), "Stream") || !strings.Contains(err.Error(), "DataKey") {
		t.Errorf("the error does not name the two fields: %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d request(s) went out before the refusal", n)
	}
}

// sheet serves the same body on every page, linking to a next one each time,
// so a walk that does not stop when the consumer does shows up as requests.
func sheet(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Link", "<"+srv.URL+"/?p="+string(rune('a'+n))+">; rel=\"next\"")
		_, _ = w.Write([]byte("a\nb\nc"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A consumer that breaks out of the loop leaves a fetcher still in its own
// loop. Every emit after that says ErrStopped and delivers nothing -- a
// second yield after false is a panic in the runtime -- and a function that
// ignores it and returns nil does not send the walk on to the next page.
func TestAnEmitAfterTheConsumerStoppedDeliversNothing(t *testing.T) {
	var calls atomic.Int32
	srv := sheet(t, &calls)

	var errs []error
	seq, err := Stream(context.Background(), core.Source{URL: srv.URL, FollowLinks: true},
		func(r core.Response, emit func(any) error) error {
			for _, line := range strings.Split(string(r.Bytes()), "\n") {
				errs = append(errs, emit(line)) // ignored, on purpose
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		got++
		break
	}

	if got != 1 {
		t.Errorf("the consumer received %d records, want 1", got)
	}
	// The break is in the body of the first record, so the emit that
	// delivered it is already the one that learns of the stop.
	if len(errs) != 3 {
		t.Fatalf("emit was called %d times, want 3", len(errs))
	}
	for i, err := range errs {
		if !errors.Is(err, core.ErrStopped) {
			t.Errorf("emit %d returned %v, want ErrStopped", i+1, err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d pages were fetched; the walk went on after the consumer stopped", n)
	}
}

// What a fetcher returns once the consumer stopped has nobody to go to: the
// stop wrapped while closing a file, or a failure of its own. Neither may be
// yielded -- a yield after false is a panic.
func TestWhatAStreamReturnsAfterTheStopIsNotYielded(t *testing.T) {
	for name, ret := range map[string]func(error) error{
		"the stop, wrapped":   func(err error) error { return fmt.Errorf("closing the workbook: %w", err) },
		"an error of its own": func(error) error { return errors.New("closing the workbook failed") },
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := sheet(t, &calls)
			seq, err := Stream(context.Background(), core.Source{URL: srv.URL},
				func(r core.Response, emit func(any) error) error {
					for {
						if err := emit("row"); err != nil {
							return ret(err)
						}
					}
				})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			for range seq {
				break
			}
		})
	}
}

// emit kept past the function's return -- handed to a goroutine, stored in
// a field -- has no consumer behind it any more. It says so and delivers
// nothing.
func TestAnEmitAfterTheStreamReturnedIsAnError(t *testing.T) {
	var calls atomic.Int32
	srv := sheet(t, &calls)

	var kept func(any) error
	seq, err := Stream(context.Background(), core.Source{URL: srv.URL},
		func(r core.Response, emit func(any) error) error {
			kept = emit
			return emit("only")
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		got++
	}

	if err := kept("late"); err == nil {
		t.Error("an emit after the Stream returned was accepted")
	}
	if got != 1 {
		t.Errorf("the consumer received %d records, want 1", got)
	}
}
