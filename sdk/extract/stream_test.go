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

// A refusal halfway through a response comes after the records already
// emitted, as the sequence's error -- still a rejection, so the log and the
// alert can tell the source from the fetcher. And a plain failure arrives
// the same way.
func TestAFailureHalfwayArrivesAfterTheRecordsAlreadyEmitted(t *testing.T) {
	for name, tc := range map[string]struct {
		fail     error
		rejected bool
	}{
		"a rejection":   {core.Reject("row 3 is a total line, not data"), true},
		"a plain error": {errors.New("the workbook is truncated"), false},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := sheet(t, &calls)
			seq, err := Stream(context.Background(), core.Source{URL: srv.URL},
				func(r core.Response, emit func(any) error) error {
					for _, row := range []string{"a", "b"} {
						if err := emit(row); err != nil {
							return err
						}
					}
					return tc.fail
				})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}

			var got []any
			var last error
			for env, err := range seq {
				if err != nil {
					last = err
					continue
				}
				got = append(got, env.Payload)
			}

			if len(got) != 2 {
				t.Errorf("the consumer received %v before the failure, want a and b", got)
			}
			if !errors.Is(last, tc.fail) {
				t.Errorf("the sequence ended with %v, want %v", last, tc.fail)
			}
			if errors.Is(last, core.ErrRejected) != tc.rejected {
				t.Errorf("errors.Is(err, ErrRejected) = %v, want %v",
					errors.Is(last, core.ErrRejected), tc.rejected)
			}
		})
	}
}

// An empty window is a result, not a failure: a 204 the function emits
// nothing for is zero records and no error.
func TestNoEmitOnA204IsZeroRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var status int
	seq, err := Stream(context.Background(), core.Source{URL: srv.URL},
		func(r core.Response, emit func(any) error) error {
			status = r.Status
			return nil
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		n++
	}
	if status != http.StatusNoContent || n != 0 {
		t.Errorf("status %d and %d records, want 204 and none", status, n)
	}
}

// emitRows emits every element of the "rows" array of a JSON page.
func emitRows(r core.Response, emit func(any) error) error {
	doc, err := r.Object()
	if err != nil {
		return err
	}
	rows, _ := doc["rows"].([]any)
	for _, row := range rows {
		if err := emit(row); err != nil {
			return err
		}
	}
	return nil
}

// OffsetKey without a PageSize advances by the rows the last page returned.
// With a Stream that is what it emitted -- there is no slice to count -- and
// an empty page is still the stop.
func TestAStreamAdvancesTheOffsetByWhatItEmitted(t *testing.T) {
	pages := map[string]string{
		"0": "a\nb\nc",
		"3": "d\ne",
		"5": "",
	}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		asked = append(asked, off)
		_, _ = w.Write([]byte(pages[off]))
	}))
	defer srv.Close()

	seq, err := Stream(context.Background(), core.Source{URL: srv.URL + "/?offset=0", OffsetKey: "offset"},
		func(r core.Response, emit func(any) error) error {
			for _, line := range strings.Split(string(r.Bytes()), "\n") {
				if line == "" {
					continue
				}
				if err := emit(line); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []string
	for env, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		got = append(got, env.Payload.(string))
	}

	if strings.Join(asked, ",") != "0,3,5" {
		t.Errorf("offsets asked = %v, want 0, 3, 5", asked)
	}
	if strings.Join(got, "") != "abcde" {
		t.Errorf("records = %v, want a to e", got)
	}
}

// CursorKey reads the next cursor out of the page while the function still
// gets the page whole, cursor and all.
func TestAStreamFollowsTheCursorAndSeesThePageWhole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("next") == "c2" { // the CursorKey names the parameter too
			_, _ = w.Write([]byte(`{"next":"","rows":["c"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"next":"c2","rows":["a","b"]}`))
	}))
	defer srv.Close()

	var bodies []string
	seq, err := Stream(context.Background(), core.Source{URL: srv.URL, CursorKey: "next"},
		func(r core.Response, emit func(any) error) error {
			bodies = append(bodies, string(r.Bytes()))
			return emitRows(r, emit)
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []string
	for env, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		got = append(got, env.Payload.(string))
	}

	if strings.Join(got, "") != "abc" {
		t.Errorf("records = %v, want a, b, c", got)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"next":"c2"`) {
		t.Errorf("the function did not see each page whole: %q", bodies)
	}
}

// MoreKey saying false ends the walk on that page, without the extra request
// an empty page would cost.
func TestAStreamStopsWhereMoreKeySaysSo(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		asked = append(asked, page)
		more := page == "1"
		_, _ = fmt.Fprintf(w, `{"more":%t,"rows":["p%s"]}`, more, page)
	}))
	defer srv.Close()

	seq, err := Stream(context.Background(),
		core.Source{URL: srv.URL, PageKey: "page", MoreKey: "more"}, emitRows)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		n++
	}

	if strings.Join(asked, ",") != "1,2" || n != 2 {
		t.Errorf("pages asked = %v and %d records, want 1, 2 and 2 records", asked, n)
	}
}
