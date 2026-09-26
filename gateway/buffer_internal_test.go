package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// Every format reports the bytes it read, because a zero here turns the size
// ceiling into one nothing ever reaches.
//
// Internal, because `decode` is internal and exporting it for a test would be
// adding public API to look at a private decision.
func TestEveryFormatMeasuresWhatItRead(t *testing.T) {
	for _, c := range []struct {
		format, body string
		events       int
	}{
		{FormatJSON, `{"id":"A"}`, 1},
		{FormatArray, `[{"id":"A"},{"id":"B","pad":"aaaaaaaaaaaa"}]`, 2},
		{FormatNDJSON, "{\"id\":\"A\"}\n{\"id\":\"B\",\"pad\":\"aaaaaaaaaaaa\"}\n", 2},
	} {
		t.Run(c.format, func(t *testing.T) {
			got, err := decode(c.format, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != c.events {
				t.Fatalf("%d events, want %d", len(got), c.events)
			}
			for i, a := range got {
				if a.bytes <= 0 {
					t.Errorf("event %d reported %d bytes", i, a.bytes)
				}
				if a.event == nil {
					t.Errorf("event %d decoded to nil", i)
				}
			}
			// Each event's own size, not one number shared out: the second
			// event in both multi-event cases is the longer one.
			if c.events == 2 && got[1].bytes <= got[0].bytes {
				t.Errorf("sizes %d and %d: the longer event did not measure longer",
					got[0].bytes, got[1].bytes)
			}
		})
	}
}

// What comes back when the pool is busy has to be counted again.
//
// `take` zeroes the buffer's byte total, so a batch handed back has to bring
// its sizes with it. Without that the ceiling silently forgets what it is
// still holding -- and it is the one ceiling whose whole job is to stop an
// OOM, so forgetting is the failure rather than an inaccuracy.
func TestBytesSurviveABatchComingBack(t *testing.T) {
	p := &pipe{
		stream: Stream{Buffer: Buffer{
			Flush:      Flush{Records: 1_000_000, Every: time.Hour},
			MaxRecords: 1_000_000,
		}},
		// Unbuffered, and nobody reading: every handoff fails and gives the
		// batch back, which is the path under test.
		queue: make(chan []sdk.Envelope),
	}

	if err := p.enqueue([]sdk.Envelope{{}, {}}, []int64{10, 20}); err != nil {
		t.Fatal(err)
	}
	if p.bytes != 30 {
		t.Fatalf("bytes = %d after 10+20", p.bytes)
	}

	p.mu.Lock()
	p.handoff(TriggerSize)
	p.mu.Unlock()

	if p.bytes != 30 {
		t.Errorf("bytes = %d after the batch came back: the ceiling forgot %d "+
			"bytes it is still holding", p.bytes, 30-p.bytes)
	}
	if len(p.sizes) != len(p.batch) {
		t.Errorf("%d sizes for %d events: the two have to stay parallel or the "+
			"next handoff hands back the wrong total", len(p.sizes), len(p.batch))
	}
}

// A flush that the pool refuses is not a flush, and must not be counted as
// one.
//
// Counting it there would report a flush per attempt on a stream whose sink is
// behind -- turning the one metric that says "you are bypassing the BigQuery
// floor" into noise exactly when the stream is in trouble.
func TestARefusedHandoffIsNotCountedAsAFlush(t *testing.T) {
	m := NewMetrics()
	p := &pipe{
		stream:  Stream{Name: "s", Buffer: Buffer{Flush: Flush{Records: 1_000_000, Every: time.Hour}, MaxRecords: 10}},
		queue:   make(chan []sdk.Envelope),
		metrics: m,
	}
	if err := p.enqueue([]sdk.Envelope{{}}, []int64{1}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.handoff(TriggerSize)
	p.mu.Unlock()

	var b strings.Builder
	if err := m.Render(&b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), MetricFlushes) {
		t.Errorf("a handoff the pool refused was counted as a flush:\n%s", b.String())
	}
}
