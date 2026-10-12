// brevis-sql-api: declared, read-only SQL served over HTTP.
//
// A BINARY OF ITS OWN and not a `brevis-sql` subcommand, for the weight
// `sql-weight.sh` guards: `brevis-sql` runs as a step in every pipeline that
// builds a model, and it must not carry what an HTTP API needs and a build
// never uses. Same module, so the connections, the dialects and the
// read-only guard are the ones the build and `serve` already trust.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/api"
	"github.com/AreteAcademy/brevis/sql/internal/connections"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

const usage = `brevis-sql-api: declared, read-only SQL over HTTP

  brevis-sql-api validate --config api.yaml [--connections brevis.yaml]
  brevis-sql-api serve    --config api.yaml [--connections brevis.yaml]
  brevis-sql-api describe --config api.yaml [--connections brevis.yaml] [--out FILE]
  brevis-sql-api key hash --tenant <tenant>   < the key on stdin

  validate  loads the file and its SQL, connecting to nothing
  serve     answers the file's endpoints
  describe  prints the manifest 'brevis endpoints publish' takes, opening nothing
  key hash  prints the line a key list holds for one key

  --config FILE       the endpoint file
  --connections FILE  the connection registry   (default brevis.yaml)
  --out FILE          describe: the manifest, written once it is whole
  --addr HOST:PORT    serve: listen here instead of the file's listen.addr
  --metrics-addr A    serve: the exposition here instead of metrics.addr
`

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(errOut, usage)
		return 2
	}
	cmd := args[0]
	if cmd == "key" {
		return keyHash(args[1:], out, errOut)
	}
	if cmd != "validate" && cmd != "serve" && cmd != "describe" {
		_, _ = fmt.Fprintf(errOut, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errOut)
	config := fs.String("config", "", "the endpoint file")
	registry := fs.String("connections", "brevis.yaml", "the connection registry")
	outFile := fs.String("out", "", "describe: write the manifest here, once it is whole, instead of stdout")
	addr := fs.String("addr", "", "serve: listen here instead of the file's listen.addr")
	metricsAddr := fs.String("metrics-addr", "", "serve: the exposition here instead of the file's metrics.addr")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *config == "" {
		_, _ = fmt.Fprintln(errOut, "--config is required: the endpoint file")
		return 2
	}

	reg, err := connections.Load(*registry)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	cfg, err := api.Load(*config, reg)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	if cmd == "describe" {
		manifest, err := api.Describe(*config, cfg, reg)
		if err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		if *outFile == "" {
			_, _ = out.Write(manifest)
			return 0
		}
		// WHOLE OR NOT AT ALL: written beside and renamed, so a publish that
		// reads the file never reads half of one.
		tmp := *outFile + ".partial"
		if err := os.WriteFile(tmp, manifest, 0o644); err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		if err := os.Rename(tmp, *outFile); err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	}
	// FROM THE ENVIRONMENT, never from a flag or the file: something that
	// could declare itself local would be something that turns off
	// authentication. The gateway states the rule; this obeys it.
	cfg.Env = os.Getenv("BREVIS_ENV")
	if cmd == "validate" {
		_, _ = fmt.Fprintf(out, "%s: %d endpoint(s) on connection %s, each one loaded and parsed\n",
			cfg.Name, len(cfg.Endpoints), cfg.Connection)
		return 0
	}
	overrideListen(cfg, *addr, *metricsAddr)
	if err := serve(cfg, out); err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func metricsMux(h *api.Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", h.Metrics())
	return mux
}

// overrideListen puts the flags' addresses over the file's, when given.
//
// FOR A CONTAINER: the file says 127.0.0.1 for a laptop, and inside a
// container that is an address nothing outside it can reach. One file for
// both, rather than a second copy that differs in one line and drifts in
// the others.
func overrideListen(cfg *api.Config, addr, metricsAddr string) {
	if addr != "" {
		cfg.Listen.Addr = addr
	}
	if metricsAddr != "" {
		cfg.Metrics.Addr = metricsAddr
	}
}

// stdin is where `key hash` reads the key; a test replaces it.
var stdin io.Reader

func keyHash(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] != "hash" {
		_, _ = fmt.Fprint(errOut, usage)
		return 2
	}
	fs := flag.NewFlagSet("key hash", flag.ContinueOnError)
	fs.SetOutput(errOut)
	tenant := fs.String("tenant", "", "the tenant the key belongs to")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	in := stdin
	if in == nil {
		in = os.Stdin
	}
	// THE KEY ON STDIN and never as an argument: an argument is in the
	// shell's history and in every `ps` while this runs.
	key, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		_, _ = fmt.Fprintln(errOut, "the key could not be read from stdin:", err)
		return 1
	}
	line, err := api.KeyLine(strings.TrimRight(key, "\r\n"), *tenant)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	_, _ = fmt.Fprintln(out, line)
	return 0
}

func serve(cfg *api.Config, out io.Writer) error {
	// ONE STREAM, `serve`'s reason: a container gives you one anyway, and an
	// audit line somewhere else is an audit line nobody collects.
	cfg.Audit = out
	h, err := api.New(cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:    cfg.Listen.Addr,
		Handler: h,
		// A HEADER THAT NEVER FINISHES holds a connection forever; a public
		// port is where somebody tries.
		ReadHeaderTimeout: 5 * time.Second,
		// AND A READER THAT NEVER FINISHES holds an answer in memory forever
		// -- found in review. A minute is past every request's own timeout.
		WriteTimeout: time.Minute,
		IdleTimeout:  2 * time.Minute,
	}
	_, _ = fmt.Fprintf(out, "brevis-sql-api %s on %s, %d endpoint(s) on connection %s\n",
		cfg.Name, cfg.Listen.Addr, len(cfg.Endpoints), cfg.Connection)
	if keys, tenants := h.Keys(); keys > 0 {
		_, _ = fmt.Fprintf(out, "  %d key(s) for %d tenant(s); each request reads as its key's tenant\n",
			keys, tenants)
	} else {
		// SAID OUT LOUD, the way `serve` says "no token": open is a state
		// somebody should be able to see without reading the file.
		_, _ = fmt.Fprintf(out, "  NO AUTHENTICATION (BREVIS_ENV=%s): every request reads as tenant %q\n",
			api.EnvLocal, api.LocalTenant)
	}
	for _, e := range cfg.Endpoints {
		if e.Tenant == "none" {
			// AND EVERY ENDPOINT THAT IGNORES THE TENANT, BY NAME. It was a
			// deliberate choice in the file; this is where an operator sees
			// it was made.
			_, _ = fmt.Fprintf(out, "  GET %s  (tenant: none -- the same rows for every key)\n", e.Path)
			continue
		}
		_, _ = fmt.Fprintf(out, "  GET %s\n", e.Path)
	}

	_, _ = fmt.Fprintln(out, "  one audit line per request, on this stream: parameter names, never values")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	failed := make(chan error, 2)
	go func() { failed <- srv.ListenAndServe() }()
	if cfg.Metrics.Addr != "" {
		// ITS OWN PORT. The API's is public by design; a /metrics there
		// would publish every endpoint's traffic to whoever holds a key.
		m := &http.Server{Addr: cfg.Metrics.Addr, Handler: metricsMux(h), ReadHeaderTimeout: 5 * time.Second}
		_, _ = fmt.Fprintf(out, "  metrics on %s/metrics, holding no tenant and no value\n", cfg.Metrics.Addr)
		go func() { failed <- m.ListenAndServe() }()
		defer func() { _ = m.Close() }()
	}
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	// DRAINED, not dropped: a rolling deploy sends SIGTERM to a replica with
	// requests in flight, and each one is somebody's answer.
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
