package api

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// A PANIC IS LOGGED BY ITS TYPE AND WHERE, NEVER ITS VALUE. `panic(row)` is
// the caller's data, and the audit line holds names and never values; where
// the hook was when it panicked is what its author needs anyway.
func TestAPanicIsLoggedByTypeAndPlaceNotValue(t *testing.T) {
	var audit bytes.Buffer
	leaky := func(context.Context, *Request) error { panic("sku-secret-4711") }
	get(beforeServer(t, &warehouse{cols: []string{"k"}}, daily(t, leaky), &audit), "/v1/daily")
	reason := lastLine(t, &audit).Reason
	if strings.Contains(reason, "sku-secret-4711") {
		t.Errorf("the panic's value reached the audit line: %s", reason)
	}
	if !strings.Contains(reason, "string") || !strings.Contains(reason, "TestAPanicIsLoggedByTypeAndPlaceNotValue") ||
		!strings.Contains(reason, "reason_test.go:") {
		t.Errorf("the reason does not say what panicked and where: %s", reason)
	}
}

// A REASON HAS A CEILING: a hook's error is its author's text, and a hook
// that puts a whole row in it does not get a whole row in every log line.
func TestAReasonIsCappedAtItsCeiling(t *testing.T) {
	var audit bytes.Buffer
	long := func(context.Context, *Request) error { return errors.New(strings.Repeat("é", 5000)) }
	get(beforeServer(t, &warehouse{cols: []string{"k"}}, daily(t, long), &audit), "/v1/daily")
	reason := lastLine(t, &audit).Reason
	if len(reason) > maxReason || !strings.HasSuffix(reason, "…") || !strings.HasPrefix(reason, "éé") {
		t.Errorf("a %d-byte reason was kept as %d bytes: %.40q…", 10000, len(reason), reason)
	}
}
