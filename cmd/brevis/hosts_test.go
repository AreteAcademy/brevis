package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/AreteAcademy/brevis/internal/config"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// The switch reaches the executor: a flag that parses into a field nobody
// passes on looks exactly like a working one (see scheduler_flags_test.go).
func TestHostHandoverReachesTheRemoteExecutor(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, on := range []bool{true, false} {
		hosts := remoteHosts(config.Config{
			Hosts: map[string]string{"box": "http://127.0.0.1:9443"}, HostToken: "t", HostHandover: on,
		}, log)
		e, ok := hosts["box"].(*remote.Executor)
		if !ok {
			t.Fatalf("host box is a %T", hosts["box"])
		}
		if e.HandOver != on {
			t.Errorf("HostHandover %v reached the executor as %v", on, e.HandOver)
		}
	}
}
