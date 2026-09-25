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
func Main(hooks *Hooks) {
	if len(os.Args) < 2 {
		log.Fatal("usage: gateway <config.yaml>")
	}
	cfg, err := Load(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	srv, err := New(cfg, hooks)
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
