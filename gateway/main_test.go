package gateway_test

import (
	"os"
	"testing"
)

// The tests build open endpoints on purpose; an unset BREVIS_ENV is
// production now, so the suite says "local" once, here. A test about the
// default sets the variable itself.
func TestMain(m *testing.M) {
	_ = os.Setenv("BREVIS_ENV", "local")
	os.Exit(m.Run())
}
