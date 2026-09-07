package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// Checker is a dependency readiness consults. A small interface on purpose
// (rule 5): postgres.Pool already satisfies it with no adapter.
type Checker interface {
	Check(ctx context.Context) error
}

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// health answers liveness: the process is alive and serving.
//
// It does NOT touch the database, on purpose. A liveness probe that depends on
// an external dependency makes Kubernetes KILL the pod when the database wobbles
// — trading a partial outage for a crashloop.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// ready responde readiness: o processo consegue atender de fato.
//
// Here it does consult the dependencies. A failure takes the pod out of the load
// balancer without killing it, which is the correct behaviour when the database
// is down.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]string, len(s.checkers))
	status := http.StatusOK

	for name, c := range s.checkers {
		if err := c.Check(r.Context()); err != nil {
			checks[name] = err.Error()
			status = http.StatusServiceUnavailable
			continue
		}
		checks[name] = "ok"
	}

	body := healthResponse{Status: "ok", Checks: checks}
	if status != http.StatusOK {
		body.Status = "unavailable"
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
