package workflow

import (
	"strings"
	"testing"
	"time"
)

const triggered = `
name: orders
steps:
  - id: build
    run: brevis-sql build
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
`

func TestATriggerSurvivesTheYAML(t *testing.T) {
	w, err := Parse("orders.yaml", []byte(triggered))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Trigger.OnLanded) != 2 {
		t.Fatalf("on_landed = %v", w.Trigger.OnLanded)
	}
	if w.Trigger.Debounce != 5*time.Minute {
		t.Errorf("debounce = %v, wanted 5m", w.Trigger.Debounce)
	}
	if !w.Trigger.Declared() {
		t.Error("Declared() is false on a workflow that declares two targets")
	}
}

// A DEBOUNCE NEEDS ITS UNIT, and yaml.v3 is the one refusing it.
//
// Pinned because the opposite would have been quiet and expensive: a bare `5`
// read as five NANOSECONDS is a window that collapses nothing, and the only
// symptom is a feature that looks like it was never built. Measured rather
// than assumed -- the first version of this file carried a comment claiming
// yaml.v3 did exactly that, and it does not.
//
// The message is yaml's own and names the LINE and the value, not the field:
//
//	line 10: cannot unmarshal !!int `5` into time.Duration
//
// Left as it is, because the gateway's `idle_timeout` and `max_conn_age` are
// time.Duration too and produce exactly this -- one wording for durations
// across the repository beats a nicer one here and a Go type name there.
func TestADebounceNeedsItsUnit(t *testing.T) {
	_, err := Parse("orders.yaml", []byte(strings.Replace(triggered, "5m", "5", 1)))
	if err == nil {
		t.Fatal("`debounce: 5` was accepted")
	}
	for _, want := range []string{"line 10", "`5`", "orders.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %s: %v", want, err)
		}
	}
}

// EVERY WORKFLOW PUBLISHED BEFORE THIS FIELD EXISTED still parses, and reads
// back as "no trigger". The field is additive in the stored document, like
// Description, Runtime, Tools and Host.
func TestAWorkflowWithNoTriggerIsUnchanged(t *testing.T) {
	w, err := Parse("plain.yaml", []byte("name: plain\nsteps:\n  - id: a\n    run: echo\n"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Trigger.Declared() {
		t.Errorf("a workflow with no `trigger:` declared %v", w.Trigger)
	}
}

// AND A BAD TARGET IS REFUSED THROUGH Parse, which is what makes `brevis
// validate` and `brevis publish` give the same answer: both reach the
// domain's invariants through here, and neither has a rule of its own.
func TestABadTargetIsRefusedThroughParse(t *testing.T) {
	bad := strings.Replace(triggered,
		"bigquery://acme-prod/bronze/orders", "postgres://user:pw@host:5432/db", 1)
	_, err := Parse("orders.yaml", []byte(bad))
	if err == nil {
		t.Fatal("a DSN in `on_landed` was accepted")
	}
	if !strings.Contains(err.Error(), "on_landed") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}
