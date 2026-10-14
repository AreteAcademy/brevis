package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/AreteAcademy/brevis/sdk"
)

// okSink takes every batch inside the window.
type okSink struct{}

func (okSink) Describe() string { return "ok" }
func (okSink) Write(_ context.Context, b []sdk.Envelope) (int64, error) {
	return int64(len(b)), nil
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	var b strings.Builder
	if err := m.Render(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func sendOne(t *testing.T, sink Sinker, m *Metrics) {
	t.Helper()
	restore := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = restore })
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 1}},
		sink:    sink,
		dead:    &recordingDead{},
		metrics: m,
	}
	_ = p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})
}

// #87: a batch whose load job finished after the gateway stopped waiting is
// delivered -- and counted apart, because how often that happens is how often
// the send window is shorter than the destination's queue, which is #85.
func TestABatchThatLandedAfterTheWindowIsCountedAsReconciled(t *testing.T) {
	m := NewMetrics()
	sendOne(t, &slowJobSink{landed: true}, m)

	out := scrape(t, m)
	if !strings.Contains(out, `brevis_gateway_reconciled_total{stream="ingestion",sink="slow-job"} 1`) {
		t.Errorf("the batch that landed after the window is not counted as reconciled:\n%s", out)
	}
}

// A batch the sink took in time was never reconciled, and a counter that
// moved for it would be the number of deliveries, which batches_total has.
func TestABatchDeliveredInTheWindowIsNotReconciled(t *testing.T) {
	m := NewMetrics()
	sendOne(t, okSink{}, m)

	out := scrape(t, m)
	if !strings.Contains(out, `outcome="delivered"} 1`) {
		t.Fatalf("the premise is gone: the batch was not delivered:\n%s", out)
	}
	if strings.Contains(out, "brevis_gateway_reconciled_total{") {
		t.Errorf("a batch delivered in the window is counted as reconciled:\n%s", out)
	}
}

// Asked and not landed -- still running, failed, never there -- is buried,
// not reconciled.
func TestABuriedBatchIsNotReconciled(t *testing.T) {
	m := NewMetrics()
	sendOne(t, &slowJobSink{landed: false}, m)

	out := scrape(t, m)
	if strings.Contains(out, "brevis_gateway_reconciled_total{") {
		t.Errorf("a buried batch is counted as reconciled:\n%s", out)
	}
}
