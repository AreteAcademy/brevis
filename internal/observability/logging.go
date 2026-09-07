// Package observability holds logging, metrics and tracing.
//
// In Phase 0 only logging exists. Metrics and tracing enter the plan's Phase 0
// as a health check only; section 32 details them for later phases, and rule 2
// forbids anticipating.
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a structured logger. JSON outside the local environment
// because that is what the collectors expect; text locally because the output is
// read by humans.
func NewLogger(env, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if env == "local" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("service", "brevis", "env", env)
}

func parseLevel(n string) slog.Level {
	switch strings.ToLower(n) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
