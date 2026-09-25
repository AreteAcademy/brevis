package gateway

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Main runs a gateway from the config named on the command line.
//
// It exists so a consumer's main is the registration and nothing else: the
// listener, the signals and the drain are the same every time, and a binary
// that has to re-derive them is a binary where one of them is forgotten.
//
// The options are where the registration goes:
//
//	func main() {
//	    sinks := gateway.NewSinks()
//	    sinks.MustRegister(postgres.Sink, postgres.New)
//	    sinks.MustRegister(files.Sink, files.New)
//	    gateway.Main(nil, gateway.WithSinks(sinks))
//	}
//
// That binary carries pgx and nothing else -- 10 MB against the 49 of one that
// registers all six. The import list is the selection, which is how
// database/sql has always worked.
func Main(hooks *Hooks, opts ...Option) {
	if len(os.Args) < 2 {
		log.Fatal("usage: gateway <config.yaml>")
	}
	cfg, err := Load(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	srv, err := New(cfg, hooks, opts...)
	if err != nil {
		log.Fatal(err)
	}

	http := &http.Server{
		Addr: cfg.Listen.Addr, Handler: srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("listening on %s", cfg.Listen.Addr)
		if err := http.ListenAndServe(); err != nil {
			log.Print(err)
		}
	}()

	// What is in a buffer at shutdown is delivered, not dropped: `memory`
	// already loses on a crash, and losing on a clean stop as well would make
	// the tier useless rather than merely limited.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = http.Shutdown(ctx)
	if err := srv.Close(ctx); err != nil {
		log.Printf("draining: %v", err)
	}
}
