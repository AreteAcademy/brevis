package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

func retrySource(url string, maxBackoff time.Duration) core.Source {
	return core.Source{
		URL: url, Method: http.MethodGet, Timeout: 5 * time.Second,
		RetryConfig: &core.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: maxBackoff},
	}
}

// A vendor's "Retry-After: 86400" is capped at MaxBackoff instead of parking
// the step for a day.
func TestRetryAfterIsCappedAtMaxBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "86400")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	start := time.Now()
	p, err := fetchPage(context.Background(), srv.Client(), retrySource(srv.URL, 50*time.Millisecond), reading{}, srv.URL, new(int64))
	if err != nil {
		t.Fatalf("fetchPage: %v", err)
	}
	p.close()
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("waited %v, expected about MaxBackoff", d)
	}
}

// A canceled context ends the retry wait at once.
func TestRetryWaitStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := fetchPage(ctx, srv.Client(), retrySource(srv.URL, time.Hour), reading{}, srv.URL, new(int64))
	if err == nil {
		t.Fatal("expected an error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("wait ignored the context: %v", d)
	}
}

// A huge error body is cut: the error carries a few KB, not the page.
func TestErrorBodyIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("x", 1<<20)))
	}))
	defer srv.Close()

	_, err := fetchPage(context.Background(), srv.Client(), retrySource(srv.URL, time.Millisecond), reading{}, srv.URL, new(int64))
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := len(err.Error()); n > 5<<10 {
		t.Errorf("error is %d bytes, expected a few KB", n)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error does not mention truncation")
	}
}
