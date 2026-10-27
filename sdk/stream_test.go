package sdk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// The point of a Stream (#140): with FlushEvery, records are written while
// the function is still reading the response, so what is alive is a batch,
// not the whole response's worth of records. Asked from inside the function:
// after twice FlushEvery records, a batch must already have reached the
// destination. A Stream that buffered behind the scenes would answer none.
func TestWithFlushEveryAStreamIsWrittenWhileItIsStillReading(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a workbook"))
	}))
	defer srv.Close()

	const flush = 3
	target := &countingTarget{}
	writtenMidway := -1
	data, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL,
		Stream: func(r sdk.Response, emit func(any) error) error {
			for i := range 10 {
				if i == 2*flush {
					writtenMidway = len(target.levas)
				}
				if err := emit(map[string]any{"i": i}); err != nil {
					return err
				}
			}
			return nil
		}}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	res, err := sdk.Load(context.Background(), data, sdk.Target{To: target, FlushEvery: flush})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if writtenMidway < 1 {
		t.Errorf("after %d records the destination had %d batch(es); the records were held "+
			"until the function returned", 2*flush, writtenMidway)
	}
	if res.Records != 10 || len(target.levas) != 4 {
		t.Errorf("Records=%d in %d batches, want 10 in 4 (3+3+3+1)", res.Records, len(target.levas))
	}
}
