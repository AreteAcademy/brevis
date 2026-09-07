package extract

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestBackoffWithoutJitterDoesNotPanic: rand.Int63n panics on a non-positive
// argument, so a RetryConfig{MaxAttempts: 5} and nothing else -- which is a
// reasonable thing to write -- took the process down on the first retry. Having
// no jitter is a choice, not a mistake.
func TestBackoffWithoutJitterDoesNotPanic(t *testing.T) {
	casos := []struct {
		name string
		cfg  core.RetryConfig
	}{
		{"so MaxAttempts", core.RetryConfig{MaxAttempts: 5}},
		{"sem jitter", core.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Second}},
		{"sem MaxBackoff", core.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, JitterFraction: 0.1}},
		{"zerado", core.RetryConfig{}},
	}
	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			for attempt := 0; attempt < 4; attempt++ {
				if d := calculateBackoff(attempt, &c.cfg); d < 0 {
					t.Errorf("backoff negativo na tentativa %d: %v", attempt, d)
				}
			}
		})
	}
}

// TestBackoffWithoutMaxBackoffDoesNotStickAtZero: with MaxBackoff zeroed the
// ceiling was zero, and every backoff was truncated to nothing -- an immediate
// retry in a loop against an API that had just returned a 429.
func TestBackoffWithoutMaxBackoffDoesNotStickAtZero(t *testing.T) {
	cfg := core.RetryConfig{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond}
	if d := calculateBackoff(1, &cfg); d < 200*time.Millisecond {
		t.Errorf("backoff = %v, esperado ao menos 200ms (2^1 x 100ms)", d)
	}
}
