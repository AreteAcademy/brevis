// A gateway with a hook: register what this binary knows, hand it the file.
//
// The registration IS the main. Everything else -- the listener, the signals,
// the drain -- is gateway.Main, so a binary with hooks of its own does not
// re-derive them and forget one.
//
//	PUBSUB_EMULATOR_HOST=localhost:8085 go run . gateway.yaml
package main

import (
	"strings"

	"github.com/AreteAcademy/brevis/gateway"
)

func main() {
	hooks := gateway.NewHooks()
	hooks.MustRegister("enrich_clicks", enrichClicks)
	gateway.Main(hooks)
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
