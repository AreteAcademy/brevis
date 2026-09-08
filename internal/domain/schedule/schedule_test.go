package schedule

import (
	"testing"
	"time"
)

func inUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

// catchup=true fills the whole gap: every missed slot becomes a run, because
// every day has a meaning of its own.
func TestCatchupTruePreencheALacuna(t *testing.T) {
	s := Schedule{
		Cron: "0 2 * * *", Timezone: "UTC", Catchup: true, Active: true,
		LastSlot: ptr(inUTC("2026-01-01T02:00:00Z")),
	}
	slots, truncado, err := s.Slots(inUTC("2026-01-05T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if truncado {
		t.Error("it should not truncate")
	}
	if len(slots) != 4 { // 02, 03, 04, 05 de janeiro
		t.Fatalf("slots = %d (%v), wanted 4", len(slots), slots)
	}
	if !slots[0].Equal(inUTC("2026-01-02T02:00:00Z")) {
		t.Errorf("primeiro slot = %v", slots[0])
	}
}

// catchup=false materializes only the most recent one: reprocessing four days
// would be waste when only the current state matters.
func TestCatchupFalseKeepsOnlyTheMostRecent(t *testing.T) {
	s := Schedule{
		Cron: "0 2 * * *", Timezone: "UTC", Catchup: false, Active: true,
		LastSlot: ptr(inUTC("2026-01-01T02:00:00Z")),
	}
	slots, _, err := s.Slots(inUTC("2026-01-05T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 {
		t.Fatalf("slots = %d (%v), wanted 1", len(slots), slots)
	}
	if !slots[0].Equal(inUTC("2026-01-05T02:00:00Z")) {
		t.Errorf("slot = %v, wanted the most recent one (05/01)", slots[0])
	}
}

// A workflow stopped for a long time with catchup=true would drown the queue. The
// limit
// corta e SINALIZA, em vez de truncar em silencio.
func TestTheLimitTruncatesAndSaysSo(t *testing.T) {
	s := Schedule{
		Cron: "0 * * * *", Timezone: "UTC", Catchup: true, Active: true,
		LastSlot: ptr(inUTC("2026-01-01T00:00:00Z")),
	}
	slots, truncado, err := s.Slots(inUTC("2026-02-01T00:00:00Z"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 10 {
		t.Errorf("slots = %d, wanted 10", len(slots))
	}
	if !truncado {
		t.Error("truncated should be true -- there were hundreds of slots")
	}
}

// A new schedule does not materialize the cron's whole history: it starts from
// now.
func TestWithNoLastSlotItCreatesNoHistory(t *testing.T) {
	s := Schedule{Cron: "0 2 * * *", Timezone: "UTC", Catchup: true, Active: true}
	slots, _, err := s.Slots(inUTC("2026-06-15T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Errorf("slots = %v; a new schedule must create nothing retroactive", slots)
	}
}

// The timezone changes the firing's instant in UTC -- which is the Brazilian
// case, where "02:00" is not 02:00Z.
func TestTheTimezoneChangesTheInstant(t *testing.T) {
	base := Schedule{Cron: "0 2 * * *", Active: true, LastSlot: ptr(inUTC("2026-06-10T00:00:00Z"))}

	utc := base
	utc.Timezone = "UTC"
	sUTC, _, err := utc.Slots(inUTC("2026-06-10T12:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}

	sp := base
	sp.Timezone = "America/Sao_Paulo"
	sSP, _, err := sp.Slots(inUTC("2026-06-10T12:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(sUTC) == 0 || len(sSP) == 0 {
		t.Fatal("expected one slot in each")
	}
	if sUTC[0].Equal(sSP[0]) {
		t.Error("02:00 in Sao Paulo cannot be the same instant as 02:00 UTC")
	}
	// Sao Paulo e UTC-3: 02:00 local = 05:00Z
	if h := sSP[0].UTC().Hour(); h != 5 {
		t.Errorf("UTC hour = %d, wanted 5", h)
	}
}

func TestAnInactiveScheduleProducesNoSlot(t *testing.T) {
	s := Schedule{
		Cron: "0 2 * * *", Timezone: "UTC", Catchup: true, Active: false,
		LastSlot: ptr(inUTC("2026-01-01T02:00:00Z")),
	}
	slots, _, err := s.Slots(inUTC("2026-01-05T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Errorf("an inactive schedule produced %d slots", len(slots))
	}
}

// The cron and the zone are validated together: a valid cron in an invalid zone
// schedules nothing, and the error would surface far from whoever wrote the
// file.
func TestValidation(t *testing.T) {
	if _, _, err := (Schedule{Cron: "invalido", Timezone: "UTC"}).Parse(); err == nil {
		t.Error("expected an invalid-cron error")
	}
	if _, _, err := (Schedule{Cron: "0 2 * * *", Timezone: "Marte/Olimpo"}).Parse(); err == nil {
		t.Error("expected an invalid-timezone error")
	}
	if _, _, err := (Schedule{Cron: "0 2 * * *"}).Parse(); err != nil {
		t.Errorf("an empty timezone should assume UTC: %v", err)
	}
}

// The author's YAML: "0 2 * * *", every day at 02:00.
func TestTheCronFromTheAuthorsExample(t *testing.T) {
	s := Schedule{Cron: "0 2 * * *", Timezone: "UTC", Active: true}
	prox, err := s.Next(inUTC("2026-03-10T23:30:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !prox.Equal(inUTC("2026-03-11T02:00:00Z")) {
		t.Errorf("next = %v, wanted 11/03 02:00Z", prox)
	}
}

// REGRESSION: with catchup=false and a gap larger than the per-cycle limit,
// the
// retorno antecipado da truncagem pulava o filtro e devolvia `limite` slots —
// making catchup=false behave like true. Found by an end-to-end test that created
// 1,100 runs where it should have created 1.
func TestCatchupFalseIsNotBypassedByTheTruncation(t *testing.T) {
	s := Schedule{
		Cron: "0 * * * *", Timezone: "UTC", Catchup: false, Active: true,
		LastSlot: ptr(inUTC("2026-01-01T00:00:00Z")),
	}
	// two months of hourly gap against a limit of 100 per cycle
	slots, truncado, err := s.Slots(inUTC("2026-03-01T00:00:00Z"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 {
		t.Fatalf("slots = %d, wanted 1 — catchup=false discards the gap", len(slots))
	}
	if truncado {
		t.Error("truncated = true; with no catchup there is nothing to truncate")
	}
	if !slots[0].Equal(inUTC("2026-03-01T00:00:00Z")) {
		t.Errorf("slot = %v, wanted the most recent one", slots[0])
	}
}

// TestTheWindowIsThePreviousSlotToThisOne.
//
// A pipeline that asks its source for [start, end) never overlaps and never
// gaps, however late the run was. Reading now() instead is the bug this
// removes.
func TestTheWindowIsThePreviousSlotToThisOne(t *testing.T) {
	for _, c := range []struct {
		cron, slot, wantStart string
	}{
		{"0 4 * * *", "2026-09-08T04:00:00Z", "2026-09-07T04:00:00Z"},    // daily
		{"*/10 * * * *", "2026-09-08T04:10:00Z", "2026-09-08T04:00:00Z"}, // every ten minutes
		{"0 * * * *", "2026-09-08T04:00:00Z", "2026-09-08T03:00:00Z"},    // hourly
		// A monthly cron needs a lookback longer than two days, which is what
		// the doubling is for.
		{"0 3 1 * *", "2026-09-01T03:00:00Z", "2026-08-01T03:00:00Z"},
		// And a yearly one longer still.
		{"0 3 1 1 *", "2026-01-01T03:00:00Z", "2025-01-01T03:00:00Z"},
	} {
		t.Run(c.cron, func(t *testing.T) {
			slot, err := time.Parse(time.RFC3339, c.slot)
			if err != nil {
				t.Fatal(err)
			}
			start, end, err := Schedule{Cron: c.cron}.Window(slot)
			if err != nil {
				t.Fatal(err)
			}
			if got := start.UTC().Format(time.RFC3339); got != c.wantStart {
				t.Errorf("start = %s, wanted %s", got, c.wantStart)
			}
			// The end is the slot itself, EXCLUDED: what arrives after it
			// belongs to the next run.
			if !end.Equal(slot) {
				t.Errorf("end = %s, wanted the slot %s", end, slot)
			}
		})
	}
}

// A timezone that observes DST does not produce a window of the wrong length
// twice a year: the cron fires at local 04:00 either way, and the interval is
// between two local 04:00s.
func TestTheWindowFollowsTheTimezone(t *testing.T) {
	// 2026-10-18 is when São Paulo would change if it still did; the assertion
	// that matters is that the window is computed IN the location rather than
	// in UTC.
	slot, err := time.Parse(time.RFC3339, "2026-09-08T07:00:00Z") // 04:00 in -03
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := Schedule{Cron: "0 4 * * *", Timezone: "America/Sao_Paulo"}.Window(slot)
	if err != nil {
		t.Fatal(err)
	}
	if got := start.UTC().Format(time.RFC3339); got != "2026-09-07T07:00:00Z" {
		t.Errorf("start = %s", got)
	}
}

// A cron nobody can parse returns the error rather than a window somebody would
// use.
func TestABrokenCronHasNoWindow(t *testing.T) {
	if _, _, err := (Schedule{Cron: "not a cron"}).Window(time.Now()); err == nil {
		t.Error("a broken cron produced a window")
	}
}
