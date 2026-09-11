// brevis-agent runs Brevis steps on a host the engine does not manage.
//
// It is the other half of `host:` in a workflow. The engine dispatches a
// command here, follows its output, and can cancel it; this machine is where
// the licence lives, or the GPU, or the data that cannot leave.
//
//	brevis-agent \
//	  --listen :9443 \
//	  --token-file /etc/brevis/agent-token \
//	  --secrets-dir /etc/brevis/secrets \
//	  --allow-secrets vendor-api,partner-sftp \
//	  --state-dir /var/lib/brevis-agent
//
// # What it will not do
//
// It is not an orchestrator. It knows nothing of workflows, dependencies,
// schedules or retries -- the engine owns all of that, and an agent that
// learned any of it would be a second orchestrator to keep in agreement with
// the first.
//
// It is not a secret store either. It RESOLVES secrets out of a directory of
// files -- the shape the kubelet mounts and Docker uses -- under an allowlist
// that belongs to THIS host. The engine sends a coordinate, never a value, and
// that is the property the whole remote executor was designed around.
//
// # What this first version does not give you
//
// Said here rather than discovered:
//
//   - ONE TOKEN for every engine. No revoking one without changing them all,
//     and no per-engine identity in the audit trail.
//   - NO TLS of its own. Put it behind something that terminates TLS, or on a
//     network where that is somebody else's job. A token on a plain socket is a
//     token anybody on the path can read.
//   - CANCEL IS BEST-EFFORT ACROSS A RESTART unless --state-dir is set, and
//     even then it refuses to signal a pid the OS has recycled rather than risk
//     killing an unrelated process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/observability"
)

func main() {
	var (
		listen     = flag.String("listen", ":9443", "address to serve on")
		tokenFile  = flag.String("token-file", "", "file holding the shared token; empty accepts any caller")
		secretsDir = flag.String("secrets-dir", "", "root of the secret store: <dir>/<name>/<key>")
		allowed    = flag.String("allow-secrets", "", "comma-separated secret names a step may ask for; empty denies every one")
		workDir    = flag.String("work-dir", "", "where a step runs when it names no directory")
		stateDir   = flag.String("state-dir", "", "where the execution -> pid map is kept, so cancel survives a restart")
		ring       = flag.Int("ring", 10000, "lines kept for a resumed connection; beyond this a reconnect fails the step")
		logLevel   = flag.String("log-level", "info", "debug, info, warn or error")
	)
	flag.Parse()

	log := observability.NewLogger("agent", *logLevel)

	token, err := readToken(*tokenFile)
	if err != nil {
		log.Error("could not read the token", "file", *tokenFile, "error", err)
		os.Exit(1)
	}
	// Warned ONCE, at startup, and not per request: a line per request would
	// bury the one that matters. An unauthenticated agent is a development
	// convenience, and it should never be a surprise in production.
	if token == "" {
		log.Warn("no --token-file: this agent accepts any caller that can reach it")
	}

	names := split(*allowed)
	if len(names) == 0 {
		// Not an error. A host that serves no secrets is a legitimate and
		// common configuration -- a box that only needs a licensed binary. It is
		// said out loud because the alternative is a step failing later with a
		// message about one secret, when the real answer is "this host serves
		// none".
		log.Info("no --allow-secrets: no step may ask this host for a secret")
	}

	a := agent.New(agent.Options{
		Token:          token,
		SecretsDir:     *secretsDir,
		AllowedSecrets: names,
		WorkDir:        *workDir,
		StateDir:       *stateDir,
		RingSize:       *ring,
	})

	srv := &http.Server{
		Addr:    *listen,
		Handler: a.Handler(log),
		// No WriteTimeout, deliberately. A step legitimately runs for hours and
		// its stream is the response; a write timeout would cut it at an
		// arbitrary point and the engine would see a broken connection it then
		// resumes, forever. What catches a dead engine is the read side going
		// away, and what catches a dead AGENT is the engine's lease.
		ReadHeaderTimeout: 10 * time.Second,
	}

	// SIGTERM stops accepting and lets the running steps finish their streams.
	// Killing them would turn a rolling restart of this agent into a batch of
	// failed runs on an engine that did nothing wrong.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Info("shutting down; running steps keep their streams until they end")
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("brevis-agent listening", "addr", *listen, "secrets", len(names),
		"ring", *ring, "state", *stateDir != "")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("the listener stopped", "error", err)
		os.Exit(1)
	}
}

// readToken reads the shared token from a file.
//
// A FILE and not a flag, because a flag is in `ps` output for anybody on the
// host to read -- which is the one place a credential must not be on a machine
// this program does not own.
func readToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("the file is empty, which is not the same as no token: "+
			"drop --token-file to accept any caller, or put one in %s", path)
	}
	return token, nil
}

func split(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
