package extract

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

func walk(t *testing.T, source core.Source) {
	t.Helper()
	seq, err := JSON(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
	}
}

// A retried POST must carry the same body as the first attempt.
func TestPostBodySurvivesRetry(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `[{"id":1}]`)
	}))
	defer srv.Close()

	const body = `{"since":"2026-01-01"}`
	walk(t, core.Source{
		URL: srv.URL, Method: http.MethodPost, Body: strings.NewReader(body),
		RetryConfig: &core.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond},
	})

	if len(bodies) != 2 || bodies[0] != body || bodies[1] != body {
		t.Fatalf("bodies = %q, want the same body twice", bodies)
	}
}

// Every page of a paged POST must carry the body.
func TestPostBodySentOnEveryPage(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if r.URL.Query().Get("page") == "3" {
			_, _ = fmt.Fprint(w, `[]`)
			return
		}
		_, _ = fmt.Fprint(w, `[{"id":1}]`)
	}))
	defer srv.Close()

	const body = `{"since":"2026-01-01"}`
	walk(t, core.Source{
		URL: srv.URL, Method: http.MethodPost, Body: strings.NewReader(body), PageKey: "page",
	})

	if len(bodies) != 3 {
		t.Fatalf("%d requests, want 3", len(bodies))
	}
	for i, b := range bodies {
		if b != body {
			t.Errorf("page %d body = %q, want %q", i+1, b, body)
		}
	}
}

// No body stays no body.
func TestNilBodyStillWorks(t *testing.T) {
	var got int64 = -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.ContentLength
		_, _ = fmt.Fprint(w, `[{"id":1}]`)
	}))
	defer srv.Close()

	walk(t, core.Source{URL: srv.URL})
	if got != 0 {
		t.Errorf("ContentLength = %d, want 0", got)
	}
}
