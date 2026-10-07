package execution

import (
	"encoding/json"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
)

// Landing is one destination a step wrote, as the step declared it:
//
//	@brevis:{"type":"landed","target":"bigquery://acme-prod/bronze/clicks","rows":48213}
//
// The Go SDK sends it after every successful load; a Python step calls
// `landed()`; dbt and shell steps echo the line. One contract for every
// language, on the pipe that already carries the phases, so the engine has one
// road for it and not one per library.
//
// Rows and Bytes are pointers because ABSENT IS NOT ZERO. A step that does not
// count says nothing, and a nil summed into a zero would draw a table that
// emptied overnight.
type Landing struct {
	Target      string
	Rows, Bytes *int64
	At          time.Time
}

// landingCeiling bounds how many DISTINCT destinations one attempt may declare.
//
// Each one becomes a row, and a step landing in a loop would otherwise write
// through the log path until Postgres noticed. It is a budget of its own, apart
// from stageCeiling: a step that lands per table must not be what pushes its
// own phases off the screen.
const landingCeiling = 100

// futureSlack is how far ahead of the engine a step's clock may be before its
// `at` is not believed. Four minutes is a skewed clock; an hour is a bug, and a
// landing dated tomorrow would keep a table "on time" until then.
const futureSlack = 5 * time.Minute

// landingClock is when a line arrived. A variable so a test can hold it still.
var landingClock = time.Now

// land records one `landed` line, or counts why it did not.
//
// Refused lines are dropped and never repaired: a target the engine fixed up
// would be a target the engine inferred, and the catalog shows only what was
// declared. They are counted rather than logged here, because the collector has
// no logger and the runner -- which knows the step -- says it once.
func (c *stageCollector) land(target string, rawRows, rawBytes json.RawMessage, at string) {
	if catalog.ValidTarget(target, false) != nil {
		c.LandingsRefused++
		return
	}
	rows, okRows := count(rawRows)
	bytes, okBytes := count(rawBytes)
	if !okRows || !okBytes {
		c.LandingsRefused++
		return
	}

	when := landingClock()
	if t, err := time.Parse(time.RFC3339Nano, at); err == nil && !t.After(when.Add(futureSlack)) {
		when = t
	}
	when = when.UTC()

	if cur, ok := c.landings[target]; ok {
		cur.Rows = add(cur.Rows, rows)
		cur.Bytes = add(cur.Bytes, bytes)
		if when.After(cur.At) {
			cur.At = when
		}
		return
	}
	if len(c.landingOrder) >= landingCeiling {
		c.LandingsOverCeiling++
		return
	}
	if c.landings == nil {
		c.landings = map[string]*Landing{}
	}
	c.landings[target] = &Landing{Target: target, Rows: rows, Bytes: bytes, At: when}
	c.landingOrder = append(c.landingOrder, target)
}

// Landings returns what this attempt declared, in the order first declared.
func (c *stageCollector) Landings() []Landing {
	out := make([]Landing, 0, len(c.landingOrder))
	for _, t := range c.landingOrder {
		out = append(out, *c.landings[t])
	}
	return out
}

// count reads an optional non-negative whole number. Absent is (nil, true);
// anything that is not a whole number of zero or more is (nil, false).
func count(raw json.RawMessage) (*int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil || n < 0 {
		return nil, false
	}
	return &n, true
}

// add sums two optional counts: absent plus absent stays absent.
func add(a, b *int64) *int64 {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	s := *a + *b
	return &s
}
