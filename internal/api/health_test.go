package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type checkerFalso struct{ err error }

func (c checkerFalso) Check(context.Context) error { return c.err }

func testServer(checkers map[string]Checker) *Server {
	return NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), checkers, nil)
}

// Liveness must not depend on the database: if it did, a Postgres wobble would
// make Kubernetes kill the pod instead of merely taking it out of the balancer.
func TestHealthIgnoresABrokenDependency(t *testing.T) {
	s := testServer(map[string]Checker{
		"postgres": checkerFalso{err: errors.New("conexao recusada")},
	})

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, wanted 200 even with the database down", rec.Code)
	}
}

func TestReadyIsOkWhenEverythingAnswers(t *testing.T) {
	s := testServer(map[string]Checker{"postgres": checkerFalso{}})

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, wanted 200", rec.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Checks["postgres"] != "ok" {
		t.Errorf("checks[postgres] = %q, wanted ok", body.Checks["postgres"])
	}
}

func TestReadyFailsAndSaysWhichDependency(t *testing.T) {
	s := testServer(map[string]Checker{
		"postgres": checkerFalso{err: errors.New("conexao recusada")},
	})

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, wanted 503", rec.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// Naming the dependency is the point: "unavailable" alone does not say where
	// to look.
	if body.Checks["postgres"] != "conexao recusada" {
		t.Errorf("checks[postgres] = %q, wanted the cause", body.Checks["postgres"])
	}
}

func TestTheWrongMethodDoesNotMatch(t *testing.T) {
	s := testServer(nil)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, wanted 405 — Go 1.22+'s ServeMux matches by method", rec.Code)
	}
}

// TestMetricsAreNotOnTheAPIsPort pins a decision that is easy to undo by
// accident and expensive to notice.
//
// This port is the one behind the Ingress and behind auth.Gate. Mounting
// /metrics here leaves two options, and both are wrong: inside the gate, no
// scraper can reach it, because the session is a cookie set by a login form;
// outside it, every workflow and step name is published to whoever finds the
// path. The endpoint lives on BREVIS_METRICS_ADDR instead, which is never in
// the Ingress.
//
// If this test starts failing, the fix is not to delete it.
func TestMetricsAreNotOnTheAPIsPort(t *testing.T) {
	rec := httptest.NewRecorder()
	testServer(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d: /metrics answered on the API's port, which is the one behind the Ingress", rec.Code)
	}
}
