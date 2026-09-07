package metrics

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Handler serves the exposition. It is an http.Handler and not a route on the
// API's mux for one reason: the API's mux is behind auth.Gate, and a scrape
// endpoint that asks for a session cookie cannot be scraped by anything normal.
//
// Punching a hole in the gate was the alternative, and it is worse. The API pod
// is the one behind an Ingress, so a free /metrics there would publish every
// workflow and step name to whoever finds the path. On its own address it is
// never in the Ingress at all, and the scheduler -- which has no HTTP server of
// its own today -- gets the same endpoint with no second design.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Buffered, so a failure mid-render does not leave a half-written 200
		// on the wire. A scraper reading truncated text does not error -- it
		// records the series it managed to parse, which is worse than nothing
		// because it looks like data.
		var body bytes.Buffer
		if err := m.WriteTo(r.Context(), &body); err != nil {
			// A failed collect means a callback failed, which means a
			// dependency is down. 500 is the honest answer: the scraper marks
			// the target down instead of recording a scrape of nothing.
			http.Error(w, "collecting metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body.Bytes())
	})
}

// Serve runs the metrics listener until the context is cancelled.
//
// It NEVER returns an error that stops the caller, and that is deliberate: a
// port already in use must not take down the scheduler. Two Brevis processes on
// one developer's machine is the common case, and killing the one that came
// second in order to protect a scrape endpoint would be the wrong trade every
// time. The failure is loud in the log and the process carries on.
func (m *Metrics) Serve(ctx context.Context, addr string, log *slog.Logger) {
	if m == nil || addr == "" {
		log.Info("metrics are off", "hint", "set BREVIS_METRICS_ADDR to serve them")
		return
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	go func() {
		log.Info("metrics listening", "addr", addr, "path", "/metrics")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics endpoint is down; the process continues without it",
				"addr", addr, "error", err)
		}
	}()
}
