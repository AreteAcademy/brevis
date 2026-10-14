package main

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/AreteAcademy/brevis/sql/api"
	internalapi "github.com/AreteAcademy/brevis/sql/internal/api"
	"github.com/AreteAcademy/brevis/sql/internal/connections"
)

// THE EXAMPLE IS A TEST. A binary somebody copies to start their own, and
// that no longer loads its own file, is a first command that fails.
func TestTheExampleLoadsWithItsHooks(t *testing.T) {
	t.Setenv("WAREHOUSE_DSN", "postgres://u:p@localhost:5432/w?sslmode=disable")
	reg, err := connections.Load("brevis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := internalapi.Load("api.yaml", reg, registered()); err != nil {
		t.Fatalf("api.yaml does not load with the hooks main registers: %v", err)
	}
}

func before(t *testing.T, query string) (url.Values, error) {
	t.Helper()
	q, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	r := &api.Request{Endpoint: "revenue", Query: q}
	return r.Query, dayFirstWindow(context.Background(), r)
}

func TestDatesWrittenDayFirstAreRewritten(t *testing.T) {
	q, err := before(t, "start_date=01/10/2026&end_date=14/10/2026")
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("start_date") != "2026-10-01" || q.Get("end_date") != "2026-10-14" {
		t.Errorf("rewritten to %v", q)
	}
}

// WHAT THE HOOK DOES NOT RECOGNISE IT LEAVES ALONE: the API's own date type
// is what refuses it, with the API's own message.
func TestAnythingElseIsLeftForTheAPI(t *testing.T) {
	q, err := before(t, "start_date=2026-10-01&end_date=yesterday")
	if err != nil || q.Get("start_date") != "2026-10-01" || q.Get("end_date") != "yesterday" {
		t.Errorf("q=%v err=%v", q, err)
	}
}

func TestAWindowWiderThanNinetyDaysIsRefused(t *testing.T) {
	_, err := before(t, "start_date=01/01/2026&end_date=14/10/2026")
	if err == nil || err.Error() != "the window is wider than 90 days; ask for less" {
		t.Fatalf("got %v", err)
	}
}

func TestAWindowOfNinetyDaysIsServed(t *testing.T) {
	if _, err := before(t, "start_date=2026-07-16&end_date=2026-10-14"); err != nil {
		t.Fatalf("90 days was refused: %v", err)
	}
}

// ROWS BECOME ONE SERIES PER SKU, in the order the query returned them.
func TestRowsBecomeASeriesPerSKU(t *testing.T) {
	r := &api.Result{
		Columns: []string{"day", "sku", "orders", "units", "revenue"},
		Rows: [][]any{
			{"2026-10-13", "a", 1, 2, 10.5},
			{"2026-10-13", "b", 3, 3, 30.0},
			{"2026-10-14", "a", 2, 4, 21.0},
		},
	}
	data, err := revenueSeries(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(data)
	want := `{"series":[{"sku":"a","points":[{"day":"2026-10-13","revenue":10.5},{"day":"2026-10-14","revenue":21}]},` +
		`{"sku":"b","points":[{"day":"2026-10-13","revenue":30}]}]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A RESULT WITHOUT THE COLUMNS THE HOOK READS IS ITS AUTHOR'S ERROR, said by
// name -- not a panic on an index.
func TestMissingColumnsAreAnError(t *testing.T) {
	_, err := revenueSeries(context.Background(), &api.Result{Columns: []string{"day"}, Rows: [][]any{{"x"}}})
	if err == nil {
		t.Fatal("a result without sku and revenue was shaped")
	}
}
