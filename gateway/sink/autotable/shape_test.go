package autotable

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The fingerprint has to be the SAME for the same document, whatever order the
// keys arrived in.
//
// Go randomises map iteration, so a plain json.Marshal of a map is a different
// byte string on every call -- and an id built on that would differ between two
// deliveries of the same event, which is the one thing it exists to prevent.
// encoding/json happens to sort a map's keys today; this test is what keeps
// that from being load-bearing, because "happens to" is how a frozen id thaws.
func TestTheFingerprintDoesNotDependOnKeyOrder(t *testing.T) {
	a := map[string]any{
		"z": 1, "a": "x", "m": map[string]any{"q": true, "b": []any{1, 2, 3}},
	}
	b := map[string]any{
		"m": map[string]any{"b": []any{1, 2, 3}, "q": true}, "a": "x", "z": 1,
	}

	first, err := fingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	// Many times, because one pass could agree by luck.
	for i := 0; i < 200; i++ {
		got, err := fingerprint(b)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("the same document fingerprinted two ways on pass %d:\n  %s\n  %s",
				i, first, got)
		}
	}
}

// Two DIFFERENT documents must not fingerprint the same, and the obvious
// shortcut does exactly that.
//
// fmt's %v on a map sorts its keys, so it looks stable enough to use -- the
// key-order test above passes with it. It is also ambiguous, because nothing
// is quoted: {"a":"b:1"} and {"a:b":"1"} both render as map[a:b:1]. Two
// unrelated events would share an id, and a `merge` would keep one of them.
//
// This is the test that makes the canonicalisation load-bearing rather than
// decorative.
func TestTwoDifferentDocumentsDoNotCollide(t *testing.T) {
	for _, c := range []struct {
		name string
		a, b map[string]any
	}{
		{"a colon that fmt cannot tell apart",
			map[string]any{"a": "b:1"}, map[string]any{"a:b": "1"}},
		{"a number and the string of it",
			map[string]any{"v": 1}, map[string]any{"v": "1"}},
		{"a nested object and its rendering",
			map[string]any{"v": map[string]any{"x": 1}}, map[string]any{"v": "map[x:1]"}},
		{"an empty string and a missing key",
			map[string]any{"a": "", "b": 1}, map[string]any{"b": 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			one, err := fingerprint(c.a)
			if err != nil {
				t.Fatal(err)
			}
			two, err := fingerprint(c.b)
			if err != nil {
				t.Fatal(err)
			}
			if one == two {
				t.Errorf("%v and %v fingerprinted the same", c.a, c.b)
			}
		})
	}
}

// An array's ORDER is significant and must not be sorted: [1,2] and [2,1] are
// different documents, and collapsing them would merge two events that are not
// the same one.
func TestArrayOrderChangesTheFingerprint(t *testing.T) {
	one, _ := fingerprint(map[string]any{"v": []any{1, 2}})
	two, _ := fingerprint(map[string]any{"v": []any{2, 1}})
	if one == two {
		t.Error("[1,2] and [2,1] fingerprinted the same, so the array was sorted")
	}
}

// The same event twice is the same id; a different event is a different one.
func TestTheIdentityIsFrozenOverTheEvent(t *testing.T) {
	e := map[string]any{"table_name": "app_orders", "amount": 10, "occurred_at": "2026-09-25T10:00:00Z"}

	first, err := identify("app_orders", e)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := identify("app_orders", map[string]any{
		"amount": 10, "occurred_at": "2026-09-25T10:00:00Z", "table_name": "app_orders",
	})
	if first != again {
		t.Errorf("the same event produced two ids:\n  %s\n  %s", first, again)
	}
	if len(first) != 36 {
		t.Errorf("the id is %q, which is not a UUID", first)
	}

	// The same content in a DIFFERENT table is a different row. Without the
	// table in the key, two producers writing the same document would collide.
	other, _ := identify("app_events", e)
	if other == first {
		t.Error("the same content in two tables produced one id")
	}
}

// A producer's own key beats the fingerprint, and changing the payload around
// it does not change the id -- which is the whole point of sending one.
func TestAnIdempotencyKeyOverridesTheFingerprint(t *testing.T) {
	a, _ := identify("app_orders", map[string]any{
		KeyField: "ord-1", "status": "pending", "occurred_at": "2026-09-25T10:00:00Z",
	})
	b, _ := identify("app_orders", map[string]any{
		KeyField: "ord-1", "status": "paid", "occurred_at": "2026-09-25T10:00:00Z",
	})
	if a != b {
		t.Error("the same idempotency_key produced two ids, so the key was ignored")
	}

	// And without a key, the same change DOES move the id.
	c, _ := identify("app_orders", map[string]any{"status": "pending"})
	d, _ := identify("app_orders", map[string]any{"status": "paid"})
	if c == d {
		t.Error("two different documents fingerprinted the same")
	}
}

// The producer's whole event lands in `data`, including the field that named
// the table: a consumer reading the row should see what was posted.
func TestTheWholeEventLandsInData(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	row, err := shape(map[string]any{
		"table_name": "app_orders", "amount": 10, "occurred_at": "2026-09-25T09:00:00Z",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	if row[ColumnIngestedAt] != "2026-09-25T10:00:00Z" {
		t.Errorf("ingested_at is %v", row[ColumnIngestedAt])
	}
	// The producer's time, kept apart from ours.
	if row[ColumnOccurredAt] != "2026-09-25T09:00:00Z" {
		t.Errorf("occurred_at is %v", row[ColumnOccurredAt])
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(row[ColumnData].(string)), &data); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	if data["table_name"] != "app_orders" || data["amount"] != float64(10) {
		t.Errorf("the event did not survive whole into data: %v", data)
	}

	// Four columns and no more. A fifth would be a column somebody has to
	// create, which is the cost this shape exists to avoid.
	if len(row) != 3 { // ingestion_id is added by the router, not by shape
		t.Errorf("shape produced %d columns: %v", len(row), row)
	}
}

// A missing occurred_at is NULL and not now(): a made-up event time is worse
// than a missing one, because nothing downstream can tell it apart from a real
// one.
func TestAMissingOccurredAtStaysMissing(t *testing.T) {
	row, _ := shape(map[string]any{"table_name": "app_orders"}, time.Now())
	if _, present := row[ColumnOccurredAt]; present {
		t.Errorf("occurred_at was invented: %v", row[ColumnOccurredAt])
	}
}

// The default pattern is the intersection of what all four databases accept
// unquoted, so a name that passes needs no quoting anywhere and cannot carry
// an injection.
func TestTheNameRulesRefuseWhatWouldBecomeDDL(t *testing.T) {
	n, err := newNames("", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "a", "ab", "App_Orders", "1orders", "app-orders", "app orders",
		"app.orders", `app";DROP TABLE x;--`, "orders;", strings.Repeat("a", 50),
	} {
		if err := n.check(bad); err == nil {
			t.Errorf("the name %q was accepted", bad)
		}
	}
	for _, good := range []string{"app_orders", "svc_x1", "abc"} {
		if err := n.check(good); err != nil {
			t.Errorf("the name %q was refused: %v", good, err)
		}
	}
}

func TestAnAllowlistNarrowsFurther(t *testing.T) {
	n, _ := newNames("", []string{"app_", "svc_"}, 0)
	if err := n.check("app_orders"); err != nil {
		t.Errorf("app_orders was refused: %v", err)
	}
	if err := n.check("other_orders"); err == nil {
		t.Error("other_orders passed an allowlist that does not include it")
	}
}

// The rate limit bounds CREATIONS in a rolling hour, and it says what to do.
func TestCreationsAreBoundedPerHour(t *testing.T) {
	n, _ := newNames("", nil, 2)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

	if err := n.admit("a", now); err != nil {
		t.Fatal(err)
	}
	if err := n.admit("b", now); err != nil {
		t.Fatal(err)
	}
	err := n.admit("c", now)
	if err == nil {
		t.Fatal("a third creation was admitted with a limit of two")
	}
	if !strings.Contains(err.Error(), "max_new_per_hour") {
		t.Errorf("the refusal does not name the setting: %v", err)
	}

	// An hour later the budget is back, because it is a ROLLING hour and not a
	// process-lifetime total.
	if err := n.admit("c", now.Add(61*time.Minute)); err != nil {
		t.Errorf("the budget did not recover after an hour: %v", err)
	}
}

// The metastore caches BOTH answers, and forgets them.
//
// Negative entries are the half people leave out: "this table does not exist"
// is the answer that saves a round trip on the hot path of a new producer
// retrying.
func TestTheMetastoreCachesBothAnswersAndExpires(t *testing.T) {
	m := newMetastore(time.Minute)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

	if _, known := m.get("app_orders", now); known {
		t.Error("an empty cache claimed to know something")
	}

	m.put("app_orders", true, now)
	if exists, known := m.get("app_orders", now); !known || !exists {
		t.Error("a positive entry did not come back")
	}
	m.put("app_missing", false, now)
	if exists, known := m.get("app_missing", now); !known || exists {
		t.Error("a negative entry did not come back")
	}

	// A table dropped by hand outside the gateway makes every entry a lie.
	// Sixty seconds of wrongness is recoverable; an hour is an incident.
	if _, known := m.get("app_orders", now.Add(2*time.Minute)); known {
		t.Error("the entry outlived its TTL")
	}
}
