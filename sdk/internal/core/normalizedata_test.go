package core

import (
	"strings"
	"testing"
)

// Off unless somebody turns it on, which is the only promise every table
// already created depends on.
func TestNormalizeDataIsOffByDefault(t *testing.T) {
	if NormalizeData() {
		t.Error("NormalizeData() is on with nothing configured")
	}
}

func TestResolveNormalizeData(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want bool
	}{
		// strconv.ParseBool's vocabulary, so `1`, `TRUE` and `True` all work
		// and nobody has to guess which spelling this one accepts.
		{"true", true}, {"TRUE", true}, {"True", true}, {"1", true}, {"t", true},
		{"false", false}, {"FALSE", false}, {"0", false},
		// Unset is false, and so is the compose line that means "not
		// configured".
		{"", false}, {"   ", false},
	} {
		t.Run("("+c.raw+")", func(t *testing.T) {
			t.Setenv(EnvNormalizeData, c.raw)
			if got := resolveNormalizeData(); got != c.want {
				t.Errorf("%q → %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

// Set and unparseable does not start.
//
// The same call as the prefix's, and for the same reason: `yes` reading as
// false would flatten nothing while the operator believes it is flattening,
// and they would find out from a table that never grew the columns.
func TestAnUnparseableNormalizeDataPanics(t *testing.T) {
	t.Setenv(EnvNormalizeData, "yes")

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("`yes` was accepted, and it is not a bool this understands")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, EnvNormalizeData) || !strings.Contains(msg, "yes") {
			t.Errorf("the panic does not name the variable and the value: %v", r)
		}
	}()
	_ = resolveNormalizeData()
}
