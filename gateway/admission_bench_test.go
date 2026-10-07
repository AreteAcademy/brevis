package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

type plainSink struct{}

func (plainSink) Describe() string                                     { return "plain" }
func (plainSink) Write(context.Context, []sdk.Envelope) (int64, error) { return 0, nil }

type benchSink struct{ table string }

func (benchSink) Describe() string                                     { return "bench" }
func (benchSink) Write(context.Context, []sdk.Envelope) (int64, error) { return 0, nil }
func (s benchSink) Measure(e map[string]any, _ int) string {
	if s.table != "" {
		return s.table
	}
	return Text(e["t"])
}

// BenchmarkAdmission is the whole accept path: decode, hook, measure, admit,
// identify, buffer. It compiles on both sides of the routing-key change, so
// the two can be compared on one machine.
func BenchmarkAdmission(b *testing.B) {
	b.Run("keyless", func(b *testing.B) { admission(b, nil) })
	b.Run("one key", func(b *testing.B) { admission(b, benchSink{table: "t"}) })
	b.Run("eight keys", func(b *testing.B) { admission(b, benchSink{}) })
}

func admission(b *testing.B, sink Sinker) {
	const per = 200
	var body strings.Builder
	for i := range per {
		fmt.Fprintf(&body, `{"t":"t%d","k":"%d","r":"2026-01-01T00:00:00Z","v":%d}`+"\n", i%8, i, i)
	}
	payload := body.String()

	p := newPipe(Stream{
		Name: "s", Path: "/s", Format: FormatNDJSON,
		Identity: Identity{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "r"},
		Buffer: Buffer{
			Queue: 1024, Workers: 0, MaxRecords: 1 << 30,
			Flush: Flush{Every: time.Hour, Records: 1 << 30},
		},
		Retry: Retry{Attempts: 1},
	}, nil, plainSink{}, nil, 1<<24, NewMetrics(), nil)
	if sink != nil {
		p.measure, _ = sink.(Measurer)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("POST", "/s", strings.NewReader(payload)))
		if w.Code != http.StatusAccepted {
			b.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}
