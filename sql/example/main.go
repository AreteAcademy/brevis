// A brevis-sql-api with hooks: register what this binary knows, hand it the
// file.
//
// The registration IS the main. Everything else -- the commands, the
// listener, the signals, the drain -- is api.Main, so a binary with code of
// its own does not re-derive them and forget one. The published
// brevis-sql-api is these lines with nothing registered, which is why it
// refuses api.yaml next door.
//
//	WAREHOUSE_DSN=postgres://brevis:brevis@localhost:55434/warehouse?sslmode=disable \
//	BREVIS_ENV=local go run . serve --config api.yaml --connections brevis.yaml
//	curl -s 'localhost:8096/v1/chart/revenue?start_date=01/10/2026&end_date=14/10/2026'
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sql/api"
)

func main() { api.Main(registered()) }

// registered is every hook this binary knows, apart from main so a test can
// load api.yaml with exactly what the binary serves it with.
func registered() *api.Hooks {
	hooks := api.NewHooks()
	hooks.MustRegisterBefore("day_first_window", dayFirstWindow)
	hooks.MustRegisterAfter("revenue_series", revenueSeries)
	return hooks
}

// maxWindow is the widest range revenue answers.
const maxWindow = 90 * 24 * time.Hour

// dayFirstWindow is a before: an ordinary Go function over the caller's
// query string.
//
// It TRANSLATES what it recognises -- dates written 14/10/2026 become the
// 2026-10-14 the date type takes -- and leaves everything else for the API,
// whose own checks still run on what this returns. Refuse is the one way it
// speaks to the caller; any other error is a 500 whose text only the log
// sees.
func dayFirstWindow(_ context.Context, r *api.Request) error {
	for _, name := range []string{"start_date", "end_date"} {
		if d, err := time.Parse("02/01/2006", r.Query.Get(name)); err == nil {
			r.Query.Set(name, d.Format(time.DateOnly))
		}
	}
	from, err1 := time.Parse(time.DateOnly, r.Query.Get("start_date"))
	to, err2 := time.Parse(time.DateOnly, r.Query.Get("end_date"))
	if err1 == nil && err2 == nil && to.Sub(from) > maxWindow {
		return api.Refuse("the window is wider than 90 days; ask for less")
	}
	return nil
}

type point struct {
	Day     any `json:"day"`
	Revenue any `json:"revenue"`
}

type series struct {
	SKU    any     `json:"sku"`
	Points []point `json:"points"`
}

// revenueSeries is an after: the rows, cut by max_rows, become the answer's
// `data` -- one series per sku, the shape a chart reads.
//
// A FUNCTION OF ITS RESULT AND NOTHING ELSE. What it returns is what the
// cache keeps, so a hook that read the clock or the network would answer
// every later caller with what was true for the first.
func revenueSeries(_ context.Context, r *api.Result) (any, error) {
	col := map[string]int{}
	for i, c := range r.Columns {
		col[c] = i
	}
	var missing []string
	for _, c := range []string{"day", "sku", "revenue"} {
		if _, found := col[c]; !found {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("revenue_series needs the columns %s, which the query does not return",
			strings.Join(missing, ", "))
	}
	var out []*series
	bySKU := map[any]*series{}
	for _, row := range r.Rows {
		sku := row[col["sku"]]
		s, seen := bySKU[sku]
		if !seen {
			s = &series{SKU: sku}
			bySKU[sku] = s
			out = append(out, s)
		}
		s.Points = append(s.Points, point{Day: row[col["day"]], Revenue: row[col["revenue"]]})
	}
	return map[string]any{"series": out}, nil
}
