package agent

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// Handler serves the three routes the engine calls.
//
// Thin on purpose: everything that decides behaviour is in the Agent, so the
// tests that matter drive it directly and this file is transport. The route
// names are remote.HTTPAgent's, and the two are the only pair that has to
// agree -- which is why a test puts them on a socket together rather than
// trusting that they do.
func (a *Agent) Handler(log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/exec", a.handleStart(log))
	mux.HandleFunc("POST /v1/exec/{id}/resume", a.handleResume(log))
	mux.HandleFunc("POST /v1/exec/{id}/cancel", a.handleCancel(log))
	return a.authenticated(mux, log)
}

// authenticated checks the shared token.
//
// Compared in constant time, because a token compared with == leaks its prefix
// to anybody who can measure a few thousand requests -- and this listener is
// reachable from wherever the engine is.
func (a *Agent) authenticated(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.opt.Token == "" {
			// Allowed and warned about at startup rather than here: a line per
			// request would bury the one that matters.
			next.ServeHTTP(w, r)
			return
		}
		given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !constantTimeEqual(given, a.opt.Token) {
			log.Warn("a request arrived with the wrong token", "from", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Agent) handleStart(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req remote.StartRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "the request is not a StartRequest: "+err.Error(), http.StatusBadRequest)
			return
		}
		stream, err := a.Start(r.Context(), req)
		if err != nil {
			// 400, not 500: every refusal above is about what was ASKED for --
			// a protocol this agent does not speak, an id already running, a
			// secret this host does not serve. The engine turns the body into a
			// failed step naming the reason.
			log.Warn("a step was refused", "execution", req.ExecutionID, "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("step started", "execution", req.ExecutionID, "workflow", req.Workflow,
			"node", req.NodeID)
		stream_(w, r, stream)
	}
}

func (a *Agent) handleResume(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req remote.Resume
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, "the request is not a Resume: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := r.PathValue("id")
		stream, err := a.Resume(id, req.After)
		if err != nil {
			var gap remote.GapError
			if errors.As(err, &gap) {
				// 410 Gone, which is what HTTPAgent reads to build a GapError
				// again on the other side. The numbers travel with it because
				// the engine's message says how far back it wanted and how far
				// back this host can go.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGone)
				_ = json.NewEncoder(w).Encode(gap)
				return
			}
			log.Warn("a resume was refused", "execution", id, "after", req.After, "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("stream resumed", "execution", id, "after", req.After, "reason", req.Reason)
		stream_(w, r, stream)
	}
}

func (a *Agent) handleCancel(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := a.Cancel(id); err != nil {
			// A refused cancel is a 409 and not a 500, and the distinction is
			// the point: nothing broke here. The most important one -- a pid the
			// OS recycled -- is this agent DECLINING to signal, and the body
			// says so, because an operator told "cancelled" about a pid that is
			// now somebody's database has no way to find out otherwise.
			log.Warn("a cancel was not carried out", "execution", id, "error", err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Info("step cancelled", "execution", id)
		w.WriteHeader(http.StatusOK)
	}
}

// stream_ copies the NDJSON out, flushing every line.
//
// Flushed per line because the engine reads this LIVE: a buffered response
// arrives when the step ends, which for a forty-minute load is forty minutes
// late and for a killed step is never. It is the same reason the SDK's marker
// lines are flushed.
func stream_(w http.ResponseWriter, r *http.Request, body io.ReadCloser) {
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
		// The engine hung up. The process goes on and the ring goes on filling,
		// which is what makes the resume possible -- so this returns rather
		// than cancelling anything.
		if r.Context().Err() != nil {
			return
		}
	}
}

// constantTimeEqual compares without leaking length or prefix through timing.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		// Length is not a secret worth protecting here -- the token's length is
		// a configuration fact, not a credential -- and returning early keeps
		// the loop below simple.
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
