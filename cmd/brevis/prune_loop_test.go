package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/AreteAcademy/brevis/internal/config"
)

// Disabled is the default: the loop must return at once and never touch the
// repository (nil here, so any use would panic).
func TestPruneLoopOffByDefault(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := pruneLoop(context.Background(), config.Config{}, nil, log); err != nil {
		t.Fatal(err)
	}
}
