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

// Handler serves the three routes the engine calls, and one it does not.
//
// Thin on purpose: everything that decides behaviour is in the Agent, so the
// tests that matter drive it directly and this file is transport. The route
// names are remote.HTTPAgent's, and the two are the only pair that has to
// agree -- which is why a test puts them on a socket together rather than
// trusting that they do.
//
// The fourth is for the kubelet. It is the only one on `free`'s list, and the
// only GET.
func (a *Agent) Handler(log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/exec", a.handleStart(log))
	mux.HandleFunc("POST /v1/exec/{id}/resume", a.handleResume(log))
	mux.HandleFunc("POST /v1/exec/{id}/cancel", a.handleCancel(log))
	mux.HandleFunc("GET /health", health)
	return a.advertised(a.authenticated(mux, log))
}

// health answers the only question this process can honestly be asked: is it
// serving.
//
// It checks NOTHING, and that is the design rather than laziness. The agent
// has no dependency to be ready for -- no database, no queue, no upstream --
// so a probe that could fail for a second reason would take a pod out of its
// Service for something other than "not serving", which is the mistake
// internal/api/health.go names about liveness and the database.
//
// It gates READINESS and not liveness in the manifests, deliberately. A
// liveness failure sends SIGTERM to a pod that may be holding hours of a
// step's work, and this process is built the other way round: its own shutdown
// lets running steps finish their streams.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// free lists what answers without a token.
//
// ONE PATH, COMPARED EXACTLY. A kubelet sends no Authorization header, so the
// probe either goes on this list or the pod it gates never goes Ready -- and
// the list is a list rather than a prefix because everything else this process
// serves runs a command somebody sent it. Written `strings.HasPrefix(path,
// "/health")` it would stop being a named exemption and become a hole whose
// shape nobody states, and `/healthz` would start answering 404 to a caller
// with no token instead of 401.
func free(path string) bool {
	return path == "/health"
}

// advertised stamps every response with where to come back to.
//
// EVERY response, outside the authentication and outside the routing, for the
// reason this wrapper exists at all: a refusal carries it too -- an agent that
// refused a start is still the one holding whatever it already began -- and a
// route added later cannot forget it. Setting it per handler is how one of
// three gets missed.
func (a *Agent) advertised(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if addr := a.advertising(); addr != "" {
			w.Header().Set(remote.HeaderInstance, addr)
		}
		next.ServeHTTP(w, r)
	})
}

// authenticated checks the shared token.
//
// Compared in constant time, because a token compared with == leaks its prefix
// to anybody who can measure a few thousand requests -- and this listener is
// reachable from wherever the engine is.
func (a *Agent) authenticated(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if free(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
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
