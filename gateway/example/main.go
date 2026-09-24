// A gateway, whole: register the hooks this binary knows, hand it the file.
//
//	PUBSUB_EMULATOR_HOST=localhost:8085 go run . gateway.yaml
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: gateway <config.yaml>")
	}
	cfg, err := gateway.Load(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	hooks := gateway.NewHooks()
	hooks.MustRegister("enrich_clicks", enrichClicks)

	srv, err := gateway.New(cfg, hooks)
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

	// What is in a buffer at shutdown is delivered, not dropped. `memory`
	// already loses on a crash; losing on a clean stop as well would make the
	// tier useless rather than merely limited.
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

// enrichClicks is a hook: an ordinary Go function.
//
// Returning nil DROPS the event, which is most of what these do. An error sends
// that one event down the dead-letter path and never fails the batch beside it.
func enrichClicks(e map[string]any) (map[string]any, error) {
	host, _ := e["host"].(string)
	if host == "" {
		return e, nil
	}
	e["tenant"] = strings.Split(host, ".")[0]
	return e, nil
}
