package sdk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/from"
)

// recorder is the whole implementation surface a consumer has to write, which
// is the argument for the interface being two methods.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) record(kind, name, value string, attrs []Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pairs := make([]string, 0, len(attrs))
	for _, a := range attrs {
		pairs = append(pairs, a.Key+"="+a.Value)
	}
	sort.Strings(pairs)
	r.lines = append(r.lines, kind+" "+name+"{"+strings.Join(pairs, ",")+"} "+value)
}

func (r *recorder) Counter(name string, v int64, attrs ...Attr) {
	r.record("counter", name, strconv.FormatInt(v, 10), attrs)
}

func (r *recorder) Histogram(name string, v float64, attrs ...Attr) {
	r.record("histogram", name, "recorded", attrs)
}

func (r *recorder) has(want string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if l == want {
			return true
		}
	}
	return false
}

func (r *recorder) hasName(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.Contains(l, " "+name+"{") {
			return true
		}
	}
	return false
}

func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func runWithMeter(t *testing.T, m Meter, target Writer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1},{"id":2}]`))
	}))
	defer srv.Close()

	_ = runPipeline(context.Background(), &Pipeline{
		Name:   "acme_orders",
		Source: Source{From: from.HTTP{URL: srv.URL}},
		Target: Target{To: target},
		Meter:  m,
	})
}

// TestTheStandardNumbersArriveWithoutWritingACounter is the design point of
// this whole feature. The SDK already tracks records, rows, bytes and
// durations, and already prints them in the result line -- a Meter only decides
// where else they go. A consumer who had to instrument a fetcher to get
// "how many rows did it load" would be instrumenting something the SDK counted
// for them.
func TestTheStandardNumbersArriveWithoutWritingACounter(t *testing.T) {
	m := &recorder{}
	runWithMeter(t, m, fakeTarget{})

	for _, want := range []string{
		"counter " + MetricRuns + "{pipeline=acme_orders,status=success} 1",
		"counter " + MetricRecords + "{pipeline=acme_orders} 2",
		"counter " + MetricRows + "{pipeline=acme_orders} 2",
		"histogram " + MetricExtract + "{pipeline=acme_orders} recorded",
		"histogram " + MetricLoad + "{pipeline=acme_orders} recorded",
	} {
		if !m.has(want) {
			t.Errorf("missing: %s", want)
		}
	}
	if t.Failed() {
		t.Logf("what the meter got:\n%s", m.dump())
	}
}

// TestARunThatFailedIsStillCounted. A failure rate that skips the failures
// looks better the worse things get, and a run that broke before producing a
// result is exactly the one it exists to include.
func TestARunThatFailedIsStillCounted(t *testing.T) {
	m := &recorder{}
	runWithMeter(t, m, fakeTarget{failure: true, rows: []string{"row 0 refused"}})

	want := "counter " + MetricRuns + "{pipeline=acme_orders,status=failed} 1"
	if !m.has(want) {
		t.Errorf("missing: %s\n%s", want, m.dump())
	}
	// And the rows the destination refused are their own number: they are the
	// reason somebody opens the run, and they are invisible in "rows loaded".
	if !m.has("counter " + MetricRowsRejected + "{pipeline=acme_orders} 1") {
		t.Errorf("the refused rows were not counted:\n%s", m.dump())
	}
}

// TestACounterThatWouldBeZeroIsNotSent.
//
// A counter receiving 0 is indistinguishable from one that was never called, so
// sending it buys nothing -- and NOT sending it keeps a fetcher that never
// paginates from owning a pages series that is permanently flat. A number that
// is always zero is worse than no number.
func TestACounterThatWouldBeZeroIsNotSent(t *testing.T) {
	m := &recorder{}
	runWithMeter(t, m, fakeTarget{})

	for _, absent := range []string{MetricRowsIgnored, MetricSourceFailed, MetricRowsRejected} {
		if m.hasName(absent) {
			t.Errorf("%s was reported for a run that had none:\n%s", absent, m.dump())
		}
	}
	// Pages is on the other side of the line and it is worth naming: one page
	// IS one page. A single-request fetch has Pages=1, not 0, so it is reported
	// -- the rule is "do not send a zero", not "do not send a small number".
	if !m.has("counter " + MetricPages + "{pipeline=acme_orders} 1") {
		t.Errorf("a one-page fetch should still report one page:\n%s", m.dump())
	}
	// The durations are the exception, and deliberately: a histogram's count is
	// how many runs happened, so dropping the fast ones biases every percentile
	// upward.
	if !m.hasName(MetricExtract) {
		t.Errorf("the extract duration was dropped:\n%s", m.dump())
	}
}

// TestAPipelineWithNoMeterRunsAsBefore. Nil is the normal case: every fetcher
// published before this existed passes no Meter, and the cost of the feature to
// them has to be exactly zero.
func TestAPipelineWithNoMeterRunsAsBefore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()

	err := runPipeline(context.Background(), &Pipeline{
		Source: Source{From: from.HTTP{URL: srv.URL}},
		Target: Target{To: fakeTarget{}},
	})
	if err != nil {
		t.Fatalf("a pipeline with no meter failed: %v", err)
	}
}

// TestNoLabelIsUnbounded mirrors the engine's cardinality rule on this side of
// the fence. The SDK's own labels are the pipeline's name and the outcome, and
// a fetcher that adds a record id to one is how a metrics bill arrives.
func TestNoLabelIsUnbounded(t *testing.T) {
	m := &recorder{}
	runWithMeter(t, m, fakeTarget{})

	for _, line := range strings.Split(m.dump(), "\n") {
		inside := line[strings.Index(line, "{")+1 : strings.Index(line, "}")]
		if inside == "" {
			continue
		}
		for _, pair := range strings.Split(inside, ",") {
			key := pair[:strings.Index(pair, "=")]
			if key != "pipeline" && key != "status" {
				t.Errorf("unexpected label %q in %s -- keep the label set bounded", key, line)
			}
		}
	}
}
