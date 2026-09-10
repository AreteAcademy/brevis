// Package api exposes Brevis's HTTP interface.
//
// It uses plain net/http. ServeMux's method-and-path routing (Go 1.22+) covers
// what is needed, and the rule is to avoid a framework when the
// stdlib does the job. This system's hard work is in the queue, in the scheduler
// and in the state machine — not in the HTTP.
package api

import (
	"github.com/AreteAcademy/brevis/internal/auth"
	"log/slog"
	"net/http"
	"time"
)

// Server carries the router and the dependencies readiness consults.
type Server struct {
	log      *slog.Logger
	checkers map[string]Checker
	mux      *http.ServeMux

	// portao wraps the mux when there is a credential. Nil = an open interface,
	// which
	// so acontece em desenvolvimento (config.Load recusa o contrario).
	gate *auth.Gate
}

// NewServer builds the router. The checkers are named so that /ready says WHICH
// dependency failed, and not merely that something did.
//
// `ui` may be nil: a process that only serves health checks does not need the
// pages, and requiring them would couple the server to the database for no
// reason.
func NewServer(log *slog.Logger, checkers map[string]Checker, ui *UI) *Server {
	return NewServerAutenticado(log, checkers, ui, auth.Credential{}, false)
}

// NewServerAutenticado is the same, requiring a session when the credential is
// configured. `inseguro` sends the cookie without the Secure flag — needed only
// for plain http in development, because a Secure cookie never comes back over
// http and the login would look simply broken.
func NewServerAutenticado(log *slog.Logger, checkers map[string]Checker, ui *UI,
	cred auth.Credential, inseguro bool,
) *Server {
	s := &Server{log: log, checkers: checkers, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("GET /ready", s.ready)
	if ui != nil {
		ui.Registrar(s.mux)
	}
	if cred.Enabled() {
		s.gate = &auth.Gate{Cred: cred, Next: s.mux, Insecure: inseguro}
		if ui != nil {
			ui.RegistrarLogin(s.mux, s.gate)
		}
	}
	return s
}

// ServeHTTP makes Server an http.Handler, with an access log.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &gravador{ResponseWriter: w, status: http.StatusOK}
	if s.gate != nil {
		s.gate.ServeHTTP(rec, r)
	} else {
		s.mux.ServeHTTP(rec, r)
	}

	s.log.Info("http",
		"method", r.Method, "path", r.URL.Path,
		"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
}

// HTTPServer returns the configured server. The timeouts exist because
// net/http's default is none: without them, a slow connection holds a handler
// indefinitely.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// gravador captures the status for the access log; http.ResponseWriter does not
// expose it after it is written.
type gravador struct {
	http.ResponseWriter
	status int
}

func (g *gravador) WriteHeader(codigo int) {
	g.status = codigo
	g.ResponseWriter.WriteHeader(codigo)
}
