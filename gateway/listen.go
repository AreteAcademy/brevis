package gateway

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Bounding how long a client keeps one connection, so load spreads across
// replicas. [#44]
//
// Behind a Kubernetes ClusterIP Service, kube-proxy chooses a backend pod
// ONCE PER TCP CONNECTION and not per request. Most HTTP clients keep
// connections alive and reuse them, so a producer with a handful of them
// sends all its traffic to the same one or two replicas however many are
// running -- and a replica the HPA adds receives nothing at all, because
// nobody dials it. The extra capacity sits idle until something unrelated
// makes the clients reconnect.
//
// A consumer measured one of six replicas taking about half the events,
// its buffer at 99% of `max_records`, while four others took under three
// events a second. The sink kept up the whole time: the pressure came from
// the imbalance alone.
//
// WHY HERE AND NOT SOMEWHERE ELSE. Fixing every client means one forgotten
// client brings it back; a service mesh is new infrastructure for one
// service; disabling keep-alive is a handshake on every request. A bounded
// connection age is the usual server-side answer, and it costs one
// in-cluster handshake per connection per age.

// connBorn is the key the accept time is stamped under.
//
// An unexported struct type, which is the standard way: a string key could
// collide with one a handler package also wrote into the request context.
type connBorn struct{}

// stampAccept records when a connection was accepted, for
// boundConnections to read.
//
// It goes on http.Server.ConnContext, which runs once per CONNECTION. A
// per-request middleware cannot do this: by the time a handler runs, the
// connection it arrived on has no age it can see.
func stampAccept(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, connBorn{}, time.Now())
}

// boundConnections ends a connection older than maxAge, after answering.
//
// AFTER ANSWERING, which is the half that makes this safe to turn on. The
// request is served normally and the response is unchanged but for the
// header; nothing is retried and no batch is lost. The client opens a
// fresh connection for its next request and kube-proxy places it again.
//
// maxAge of zero is OFF, and that is the default: how long a client may
// keep one connection is policy, and policy belongs to whoever operates
// the deployment. `idle_timeout` is the opposite -- see Config.IdleTimeout.
//
// A connection with NO STAMP is left alone rather than treated as
// infinitely old. Without that, a server built without ConnContext would
// close every connection it ever answered on, which is a handshake per
// request -- the outcome this exists to avoid.
func boundConnections(next http.Handler, maxAge time.Duration) http.Handler {
	if maxAge <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		born, stamped := r.Context().Value(connBorn{}).(time.Time)
		if stamped && time.Since(born) >= maxAge {
			// net/http reads this before writing the response and closes
			// the connection once the body is done. Setting it here rather
			// than calling anything means the handler below still owns the
			// status and the body.
			w.Header().Set("Connection", "close")
		}
		next.ServeHTTP(w, r)
	})
}

// listener builds an http.Server with this gateway's timeouts on it.
//
// ONE FUNCTION FOR BOTH LISTENERS. The report named the ingest server and
// the metrics server together, and they had the same two zeros -- which is
// what two `&http.Server{…}` literals written months apart produce. A
// second place to set a timeout is a second place to forget one.
//
// ConnContext is set on both even though only the ingest handler is
// wrapped: it costs a time.Now per connection, and a server that stamps
// nothing is a server where turning the age on later does nothing and says
// nothing about why.
func listener(cfg *Config, addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: h,

		// Unchanged, and still the only bound on a client that opens a
		// connection and sends nothing.
		ReadHeaderTimeout: 5 * time.Second,

		// The correction. See Config.IdleTimeout: both this and
		// ReadTimeout were zero, so Go had no fallback and a connection
		// had no timeout at all. [#44]
		IdleTimeout: cfg.IdleTimeout(),

		ConnContext: stampAccept,
	}
}
