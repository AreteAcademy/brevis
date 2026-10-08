package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func withTrigger(tr Trigger) Workflow {
	return Workflow{
		Slug:    "orders",
		Nodes:   []Node{{ID: "build", Run: "brevis-sql build"}},
		Trigger: tr,
	}
}

// A WORKFLOW WITH NO TRIGGER IS UNTOUCHED. Everything published before this
// field existed reads back with the zero value, which is "no trigger", and
// has to validate exactly as it did.
func TestNoTriggerIsStillValid(t *testing.T) {
	if err := withTrigger(Trigger{}).Validate(); err != nil {
		t.Errorf("a workflow with no trigger was refused: %v", err)
	}
}

func TestATriggerNamesTargetsAndAWindow(t *testing.T) {
	w := withTrigger(Trigger{
		OnLanded: []string{"bigquery://acme-prod/bronze/orders"},
		Debounce: 5 * time.Minute,
	})
	if err := w.Validate(); err != nil {
		t.Fatalf("a good trigger was refused: %v", err)
	}
}

// A PATTERN IS ADMITTED, and that is not a loosening. `auto_table` creates a
// table per route, so a gateway's manifest can name the dataset they land in
// and not the tables -- `bigquery://p/bronze/*` is the only subscription that
// can cover them, and the catalog already admits that form for a manifest.
func TestAPatternIsASubscription(t *testing.T) {
	w := withTrigger(Trigger{OnLanded: []string{"bigquery://acme-prod/bronze/*"}})
	if err := w.Validate(); err != nil {
		t.Errorf("a pattern was refused: %v", err)
	}
}

// Each refusal NAMES the thing it refused, because a trigger is one line in a
// file somebody is editing and "invalid trigger" sends them to read all of it.
func TestABadTriggerIsRefusedByName(t *testing.T) {
	for _, c := range []struct {
		name string
		tr   Trigger
		says string
	}{
		{
			name: "a debounce with no targets",
			tr:   Trigger{Debounce: time.Minute},
			says: "on_landed",
		},
		{
			name: "a negative debounce",
			tr:   Trigger{OnLanded: []string{"bigquery://p/d/t"}, Debounce: -time.Minute},
			says: "debounce",
		},
		{
			// A DSN pasted where a name belongs: the exact thing the
			// catalog's own check exists to refuse, and the reason a target
			// is an identity and never an address.
			name: "a target carrying a credential",
			tr:   Trigger{OnLanded: []string{"postgres://user:pw@host:5432/db"}},
			says: "postgres://",
		},
		{
			name: "an empty target",
			tr:   Trigger{OnLanded: []string{"  "}},
			says: "on_landed",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := withTrigger(c.tr).Validate()
			if err == nil {
				t.Fatalf("accepted %+v", c.tr)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not say %q: %v", c.says, err)
			}
			if !strings.Contains(err.Error(), "orders") {
				t.Errorf("the refusal does not name the workflow: %v", err)
			}
		})
	}
}

// THE STORED DOCUMENT IS JSON OF THIS STRUCT, so "additive" is a claim about
// encoding/json and not a wish. Two directions, both of which have to hold:
//
//   - a document written BEFORE this field existed has no key for it, and
//     reads back as the zero value, which is "no trigger"
//   - a document written now round-trips, debounce included
//
// An older engine reading a document written by this one ignores the key it
// does not know, which is the same mechanism Description, Runtime, Tools and
// Host rely on.
func TestTheTriggerIsAdditiveInTheStoredDocument(t *testing.T) {
	// What a document from before today looks like.
	var old Workflow
	if err := json.Unmarshal([]byte(`{"Slug":"orders","Nodes":[{"ID":"a","Run":"echo"}]}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Trigger.Declared() || old.Trigger.Debounce != 0 {
		t.Errorf("an older document read back a trigger: %+v", old.Trigger)
	}
	if err := old.Validate(); err != nil {
		t.Errorf("an older document no longer validates: %v", err)
	}

	// And one written now.
	w := withTrigger(Trigger{
		OnLanded: []string{"bigquery://acme-prod/bronze/orders"},
		Debounce: 5 * time.Minute,
	})
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var back Workflow
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Trigger.Debounce != 5*time.Minute {
		t.Errorf("debounce came back %v, not 5m -- %s", back.Trigger.Debounce, raw)
	}
	if len(back.Trigger.OnLanded) != 1 {
		t.Errorf("on_landed came back %v", back.Trigger.OnLanded)
	}
}
