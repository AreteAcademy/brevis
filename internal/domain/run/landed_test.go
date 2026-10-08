package run

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A RUN STARTED BY A LANDING SAYS WHAT LANDED.
//
// #63: "the run receives the triggering landings as auto params". The targets
// and nothing else -- a step that wants the row counts reads /data, and Env()
// is a flat map every language has to parse.
func TestARunKnowsWhatLandedToStartIt(t *testing.T) {
	slot := time.Date(2026, 3, 11, 4, 5, 0, 0, time.UTC)
	r := Run{
		TriggerType:    "landed",
		LogicalDate:    &slot,
		TriggerTargets: []string{"bigquery://acme-prod/bronze/orders", "bigquery://acme-prod/bronze/items"},
	}
	a := Auto(r, slot.Add(time.Second), Previous{}, Interval{})

	if len(a.Landed) != 2 || a.Landed[0] != "bigquery://acme-prod/bronze/orders" {
		t.Fatalf("Landed = %v", a.Landed)
	}

	env := a.Env()
	if got := env["BREVIS_AUTO_LANDED"]; got != "bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items" {
		t.Errorf("BREVIS_AUTO_LANDED = %q", got)
	}
	// And the exact list is in the JSON, which is what a reader uses when a
	// target could hold the separator.
	var back AutoParams
	if err := json.Unmarshal([]byte(env["BREVIS_AUTO_PARAMS"]), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Landed) != 2 {
		t.Errorf("the JSON lost the list: %v", back.Landed)
	}
}

// AND A RUN THAT NO LANDING STARTED SETS NOTHING.
//
// The optional variables are OMITTED rather than set empty -- the rule Env()
// already states for the timestamps. A variable that is always there and
// sometimes blank makes every reader write the same two-line check.
func TestAScheduledRunHasNoLandedVariable(t *testing.T) {
	slot := time.Date(2026, 3, 11, 4, 0, 0, 0, time.UTC)
	a := Auto(Run{TriggerType: "schedule", LogicalDate: &slot}, slot, Previous{}, Interval{})

	if a.Landed != nil {
		t.Errorf("Landed = %v on a scheduled run", a.Landed)
	}
	if _, set := a.Env()["BREVIS_AUTO_LANDED"]; set {
		t.Error("BREVIS_AUTO_LANDED was set on a run no landing started")
	}
	// And it is absent from the JSON too, rather than present as null.
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "landed") {
		t.Errorf("the JSON carries an empty landed: %s", raw)
	}
}
