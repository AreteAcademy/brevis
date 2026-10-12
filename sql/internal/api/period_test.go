package api

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

func periodEndpoint(t *testing.T, loc *time.Location, now time.Time) *Endpoint {
	t.Helper()
	e := &Endpoint{Name: "e", Path: "/v1/e", Tenant: "none",
		Params: map[string]Param{
			"period":     {Type: "period", Default: ptr("last_7_days")},
			"start_date": {Type: "date"},
			"end_date":   {Type: "date"},
		},
		SQL: "select 1 where d between {{ start_date }} and {{ end_date }}",
	}
	if err := e.check(); err != nil {
		t.Fatal(err)
	}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	e.loc, e.now = loc, func() time.Time { return now }
	return e
}

func day(y int, m time.Month, d int) dialect.Date { return dialect.Date{Year: y, Month: m, Day: d} }

func rangeOf(t *testing.T, e *Endpoint, query string) (dialect.Date, dialect.Date) {
	t.Helper()
	vals, err := valuesFor(e, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	_, args, err := e.Template.Render(vals, postgres.Dialect{})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return args[0].Value.(dialect.Date), args[1].Value.(dialect.Date)
}

// EVERY PERIOD, ON A FIXED CLOCK: Wednesday 2026-10-14, midday UTC.
//
// `last_7_days` and `last_30_days` are COMPLETE days and not today: the data
// behind an endpoint arrives in batches, today is always partial, and a
// "last seven days" that includes a third of today reads as a drop on the
// last bar of every chart. Weeks are ISO, Monday first.
func TestEachPeriodIsTheDaysItNames(t *testing.T) {
	e := periodEndpoint(t, time.UTC, time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC))
	for period, want := range map[string][2]dialect.Date{
		"today":         {day(2026, 10, 14), day(2026, 10, 14)},
		"yesterday":     {day(2026, 10, 13), day(2026, 10, 13)},
		"last_7_days":   {day(2026, 10, 7), day(2026, 10, 13)},
		"last_30_days":  {day(2026, 9, 14), day(2026, 10, 13)},
		"last_week":     {day(2026, 10, 5), day(2026, 10, 11)},
		"last_month":    {day(2026, 9, 1), day(2026, 9, 30)},
		"month_to_date": {day(2026, 10, 1), day(2026, 10, 14)},
	} {
		from, to := rangeOf(t, e, "period="+period)
		if from != want[0] || to != want[1] {
			t.Errorf("%s is %s..%s, wanted %s..%s", period, from, to, want[0], want[1])
		}
	}
}

// LAST MONTH IN JANUARY IS DECEMBER OF LAST YEAR, and last week on a Monday
// is the seven days before it -- the two edges a hand-rolled date sum gets
// wrong first.
func TestPeriodsAcrossTheirEdges(t *testing.T) {
	e := periodEndpoint(t, time.UTC, time.Date(2027, time.January, 4, 0, 0, 0, 0, time.UTC)) // a Monday
	if from, to := rangeOf(t, e, "period=last_month"); from != day(2026, 12, 1) || to != day(2026, 12, 31) {
		t.Errorf("last_month in January: %s..%s", from, to)
	}
	if from, to := rangeOf(t, e, "period=last_week"); from != day(2026, 12, 28) || to != day(2027, 1, 3) {
		t.Errorf("last_week on a Monday: %s..%s", from, to)
	}
}

// TODAY IS THE FILE'S TODAY. At 04:30 UTC on 2026-03-08 it is still the 7th
// in New York -- and that is the night the clocks change there, which is
// where an implementation adding 24-hour durations goes a day wrong.
func TestTodayIsTheTimezonesToday(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database here")
	}
	e := periodEndpoint(t, ny, time.Date(2026, time.March, 8, 4, 30, 0, 0, time.UTC))
	if from, _ := rangeOf(t, e, "period=today"); from != day(2026, 3, 7) {
		t.Errorf("today in New York is %s", from)
	}
	e = periodEndpoint(t, ny, time.Date(2026, time.March, 9, 12, 0, 0, 0, time.UTC))
	if from, to := rangeOf(t, e, "period=yesterday"); from != day(2026, 3, 8) || to != from {
		t.Errorf("yesterday across the change is %s..%s", from, to)
	}
}

func TestExplicitDatesWinAndMustComeAsAPair(t *testing.T) {
	e := periodEndpoint(t, time.UTC, time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC))
	if from, to := rangeOf(t, e, "period=today&start_date=2026-01-01&end_date=2026-01-31"); from != day(2026, 1, 1) || to != day(2026, 1, 31) {
		t.Errorf("explicit dates lost to the period: %s..%s", from, to)
	}
	for query, says := range map[string]string{
		"start_date=2026-01-01":                     "end_date",
		"end_date=2026-01-01":                       "start_date",
		"start_date=2026-02-01&end_date=2026-01-01": "after",
		"period=last_year":                          "today, yesterday",
	} {
		if _, err := valuesFor(e, query); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: %v (wanted it to mention %q)", query, err, says)
		}
	}
}

func TestAPeriodNeedsItsTwoDatesAndIsNeverAValue(t *testing.T) {
	for name, c := range map[string]struct {
		params map[string]Param
		sql    string
		says   string
	}{
		"no end_date": {map[string]Param{"period": {Type: "period"}, "start_date": {Type: "date"}},
			"select {{ start_date }}", "end_date"},
		"start_date not a date": {map[string]Param{"period": {Type: "period"}, "start_date": {Type: "string", MaxLen: 10}, "end_date": {Type: "date"}},
			"select {{ start_date }}", "start_date"},
		"two periods": {map[string]Param{"period": {Type: "period"}, "p2": {Type: "period"}, "start_date": {Type: "date"}, "end_date": {Type: "date"}},
			"select {{ start_date }}", "one period"},
		"the period itself in the SQL": {map[string]Param{"period": {Type: "period"}, "start_date": {Type: "date"}, "end_date": {Type: "date"}},
			"select {{ period }}", "start_date and end_date"},
	} {
		e := &Endpoint{Name: "e", Path: "/v1/e", Tenant: "none", Params: c.params, SQL: c.sql}
		err := e.check()
		if err == nil {
			err = e.parse(postgres.Dialect{})
		}
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v (wanted it to mention %q)", name, err, c.says)
		}
	}
}

func TestATimezoneThatDoesNotExistIsRefused(t *testing.T) {
	_, err := fixture(t, strings.Replace(good, "connection: warehouse", "connection: warehouse\ntimezone: Mars/Olympus", 1),
		map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"})
	if err == nil || !strings.Contains(err.Error(), "Mars/Olympus") {
		t.Errorf("got %v", err)
	}
}

// start_date AS SOMETHING ELSE, ON AN ENDPOINT WITH NO PERIOD, is just a
// parameter with that name: there is no order to check and nothing to panic
// over.
func TestDatesNamedLikeAPeriodsButTypedOtherwiseAreLeftAlone(t *testing.T) {
	e := typed(t, map[string]Param{
		"start_date": {Type: "string", MaxLen: 10},
		"end_date":   {Type: "string", MaxLen: 10},
	}, "select {{ start_date }}, {{ end_date }}")
	if _, err := valuesFor(e, "start_date=b&end_date=a"); err != nil {
		t.Errorf("refused: %v", err)
	}
}
