package gateway

import (
	"reflect"
	"strings"
	"testing"
)

// A refused request is counted once, by reason, with the stream it was for.
//
// Requests and not events, and that is the shape rather than a convenience: a
// body that never decoded has no event count to attribute, which is why
// `rejected` -- counted per event, after decode -- structurally cannot cover
// these, and why `saturated` already counts requests.
func TestARefusedRequestIsCountedByReason(t *testing.T) {
	m := NewMetrics()
	m.count(m.refused, 1, "clicks", ReasonBodyTooLarge)
	m.count(m.refused, 1, "clicks", ReasonMalformed)
	m.count(m.refused, 1, "clicks", ReasonMalformed)

	out := renderInternal(t, m)
	for _, want := range []string{
		`brevis_gateway_requests_refused_total{stream="clicks",reason="body_too_large"} 1`,
		`brevis_gateway_requests_refused_total{stream="clicks",reason="malformed"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the scrape is missing:\n  %s\ngot:\n%s", want, out)
		}
	}
}

// EVERY COUNTER FIELD IS ENROLLED, counted rather than listed.
//
// Render used to walk a slice of fields typed out by hand, and a counter added
// to the struct and forgotten there compiled, incremented on every request and
// never reached a scrape. `newCounters` enrols now, so the two cannot
// disagree — and this is what says so, because "cannot" is a claim and claims
// get refactored.
//
// It reflects to COUNT, never to read: reflect refuses Interface() on an
// unexported field, and IsNil is all this needs.
func TestEveryCounterFieldIsEnrolled(t *testing.T) {
	m := NewMetrics()

	v := reflect.ValueOf(m).Elem()
	counterPtr := reflect.TypeOf((*counters)(nil))

	var fields, live int
	for i := range v.NumField() {
		if v.Type().Field(i).Type != counterPtr {
			continue
		}
		fields++
		if !v.Field(i).IsNil() {
			live++
		}
	}

	if fields < 9 {
		t.Fatalf("reflected over %d counter fields, fewer than this package has", fields)
	}
	// The volume pair is nil unless BREVIS_INGESTION_METRICS, and a nil
	// instrument is "not configured" rather than "broken".
	if live != len(m.counters) {
		t.Errorf("%d counter fields are built and %d are enrolled for the scrape: "+
			"one was made without Metrics.newCounters, so it will never render",
			live, len(m.counters))
	}
}

// The histograms have the same shape and the same trap.
func TestEveryHistogramFieldIsEnrolled(t *testing.T) {
	m := NewMetrics()

	v := reflect.ValueOf(m).Elem()
	histPtr := reflect.TypeOf((*histograms)(nil))

	var live int
	for i := range v.NumField() {
		if v.Type().Field(i).Type == histPtr && !v.Field(i).IsNil() {
			live++
		}
	}
	if live == 0 {
		t.Fatal("no histogram fields found; this test is looking at the wrong struct")
	}
	if live != len(m.histograms) {
		t.Errorf("%d histogram fields are built and %d are enrolled", live, len(m.histograms))
	}
}
