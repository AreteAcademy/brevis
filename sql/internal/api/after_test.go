package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

var dayGrain = "day"

// volume is an endpoint with one parameter of each type, and the after
// under test.
func volume(t *testing.T, after After) *Endpoint {
	t.Helper()
	e := &Endpoint{
		Name: "volume", Path: "/v1/volume",
		Params: map[string]Param{
			"grain": {Type: "enum", Values: []string{"day", "week"}, Default: &dayGrain},
			"top":   {Type: "int"},
			"day":   {Type: "date"},
			"open":  {Type: "bool"},
		},
		SQL: "select day, n as {{ grain }} from t where w = {{ tenant }}" +
			"{% if top %} and r < {{ top }}{% endif %}" +
			"{% if day %} and d = {{ day }}{% endif %}" +
			"{% if open %} and o = {{ open }}{% endif %}",
	}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	e.after = after
	return e
}

// series turns rows of (day, n) into the shape a chart reads.
func series(_ context.Context, r *Result) (any, error) {
	points := make([]map[string]any, 0, len(r.Rows))
	for _, row := range r.Rows {
		points = append(points, map[string]any{"x": row[0], "y": row[1]})
	}
	return map[string]any{"series": points}, nil
}

// THE HOOK'S VALUE IS THE DATA, AND ONLY THE DATA: `truncated` and `meta`
// stay the API's, so every endpoint answers in one envelope and the console
// can read any of them.
func TestAnAfterReplacesTheDataAndKeepsTheEnvelope(t *testing.T) {
	w := &warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-13", 4}, {"2026-10-14", 9}}}
	rec := get(beforeServer(t, w, volume(t, series), nil), "/v1/volume?grain=day")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := `{"data":{"series":[{"x":"2026-10-13","y":4},{"x":"2026-10-14","y":9}]},"truncated":false,"meta":`
	if !strings.HasPrefix(rec.Body.String(), want) {
		t.Errorf("the body is %s", rec.Body)
	}
	var parsed struct {
		Meta struct {
			Endpoint string `json:"endpoint"`
			Rows     int    `json:"rows"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// meta.rows IS WHAT THE HOOK WAS GIVEN: what the data was made from.
	if parsed.Meta.Endpoint != "volume" || parsed.Meta.Rows != 2 {
		t.Errorf("meta is %+v", parsed.Meta)
	}
}

// THE PARAMETERS ARRIVE TYPED, and in types a binary outside this module
// can name: an enum as its string, a date as the YYYY-MM-DD it was sent as.
// The tenant is Result.Tenant, not a parameter.
func TestAnAfterSeesTheRowsAndTheTypedParameters(t *testing.T) {
	w := &warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-14", 9}}}
	var seen Result
	look := func(_ context.Context, r *Result) (any, error) { seen = *r; return nil, nil }
	rec := get(beforeServer(t, w, volume(t, look), nil), "/v1/volume?grain=week&top=5&day=2026-10-14&open=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := map[string]any{"grain": "week", "top": int64(5), "day": "2026-10-14", "open": true}
	if !reflect.DeepEqual(seen.Params, want) {
		t.Errorf("the hook saw params %#v", seen.Params)
	}
	if seen.Endpoint != "volume" || seen.Tenant != LocalTenant || seen.Truncated ||
		!reflect.DeepEqual(seen.Columns, []string{"day", "n"}) || len(seen.Rows) != 1 {
		t.Errorf("the hook saw %+v", seen)
	}
}

// THE ROWS ARE CUT BEFORE THE HOOK SEES THEM, and it is told: a chart drawn
// from a cut answer should be able to say so.
func TestAnAfterIsGivenRowsCutByTheCeilingAndToldSo(t *testing.T) {
	w := &warehouse{cols: []string{"day", "n"}, rows: [][]any{{"a", 1}, {"b", 2}, {"c", 3}}}
	e := volume(t, nil)
	var given int
	var cut bool
	e.after = func(_ context.Context, r *Result) (any, error) {
		given, cut = len(r.Rows), r.Truncated
		return len(r.Rows), nil
	}
	e.MaxRows = 2
	rec := get(beforeServer(t, w, e, nil), "/v1/volume")
	if given != 2 || !cut {
		t.Errorf("the hook was given %d rows, truncated=%v", given, cut)
	}
	if !strings.HasPrefix(rec.Body.String(), `{"data":2,"truncated":true,`) {
		t.Errorf("the body is %s", rec.Body)
	}
}

// failedAfter asserts a 500 that names nothing of the failure, and an audit
// line that has it.
func failedAfter(t *testing.T, after After, reason string) {
	t.Helper()
	w := &warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-14", 9}}}
	var audit bytes.Buffer
	rec := get(beforeServer(t, w, volume(t, after), &audit), "/v1/volume")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "data") || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("the body says too much: %s", rec.Body)
	}
	if line := lastLine(t, &audit); line.Hook != "after" || !strings.Contains(line.Reason, reason) {
		t.Errorf("the audit line is %+v", line)
	}
}

func TestAnAfterErrorIsA500WithTheReasonOnlyInTheAudit(t *testing.T) {
	failedAfter(t, func(context.Context, *Result) (any, error) {
		return nil, errors.New("pricing table unreachable at 10.0.0.7")
	}, "10.0.0.7")
}

func TestAnAfterPanicIsA500(t *testing.T) {
	failedAfter(t, func(_ context.Context, r *Result) (any, error) { return r.Rows[7], nil }, "panic")
}

func TestAnAfterValueThatIsNotJSONIsA500(t *testing.T) {
	failedAfter(t, func(context.Context, *Result) (any, error) { return make(chan int), nil }, "JSON")
}

// max_response_bytes HOLDS OVER WHAT THE HOOK MADE. Its output has no rows
// to stop at, so an answer over the ceiling is not cut but refused: half of
// a shape the author chose is not a shape anybody can read.
func TestAnAfterOverTheResponseCeilingIsA500(t *testing.T) {
	w := &warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-14", 9}}}
	e := volume(t, func(context.Context, *Result) (any, error) { return strings.Repeat("x", 500), nil })
	e.maxResponse = 200
	var audit bytes.Buffer
	rec := get(beforeServer(t, w, e, &audit), "/v1/volume")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if line := lastLine(t, &audit); line.Hook != "after" || !strings.Contains(line.Reason, "max_response_bytes") {
		t.Errorf("the audit line is %+v", line)
	}
}

func TestAnAfterUnderTheResponseCeilingIsServed(t *testing.T) {
	w := &warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-14", 9}}}
	e := volume(t, func(context.Context, *Result) (any, error) { return "small", nil })
	e.maxResponse = 200
	if rec := get(beforeServer(t, w, e, nil), "/v1/volume"); rec.Code != http.StatusOK || len(rec.Body.Bytes()) > 200 {
		t.Fatalf("status %d, %d bytes", rec.Code, len(rec.Body.Bytes()))
	}
}
