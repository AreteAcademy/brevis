package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

func wideRows(n, width int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = []any{i, strings.Repeat("x", width)}
	}
	return rows
}

type answer struct {
	Data      []map[string]any `json:"data"`
	Truncated bool             `json:"truncated"`
	Meta      struct {
		Rows int `json:"rows"`
	} `json:"meta"`
}

// A CEILING IN BYTES, beside the one in rows. Found in review: max_rows
// bounds how many rows and nothing bounds how wide, so ten thousand rows of
// a wide text column is an answer held in memory, compressed and cached
// whole. The answer stops at the last row that fits, says it was cut, and
// counts what it kept -- and it is still JSON.
func TestTheAnswerStopsAtTheLastRowThatFits(t *testing.T) {
	res := dialect.Result{Columns: []string{"k", "v"}, Rows: wideRows(50, 100)}
	body, kept, err := encode("e", res, false, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 2048 {
		t.Errorf("%d bytes over a ceiling of 2048", len(body))
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	if !a.Truncated || len(a.Data) == 0 || len(a.Data) >= 50 || a.Meta.Rows != len(a.Data) || kept != len(a.Data) {
		t.Errorf("truncated=%v rows=%d meta=%d kept=%d", a.Truncated, len(a.Data), a.Meta.Rows, kept)
	}
}

func TestAnAnswerUnderTheCeilingIsWhole(t *testing.T) {
	res := dialect.Result{Columns: []string{"k", "v"}, Rows: wideRows(3, 10)}
	body, kept, err := encode("e", res, false, 1<<20)
	var a answer
	if err != nil || json.Unmarshal(body, &a) != nil || a.Truncated || kept != 3 || len(a.Data) != 3 {
		t.Errorf("%s %v", body, err)
	}
}

// ONE ROW WIDER THAN THE CEILING IS NO ROWS, cut and said so -- not a row
// half written and not an error the caller cannot act on.
func TestARowWiderThanTheCeilingIsNoRows(t *testing.T) {
	res := dialect.Result{Columns: []string{"k", "v"}, Rows: wideRows(1, 5000)}
	body, kept, err := encode("e", res, false, 1024)
	var a answer
	if err != nil || json.Unmarshal(body, &a) != nil || !a.Truncated || kept != 0 || len(a.Data) != 0 || len(body) > 1024 {
		t.Errorf("%d bytes %s %v", len(body), body, err)
	}
}

// THROUGH THE SERVER, and a cut answer is not cached: it is not the answer.
func TestTheServerCutsByBytesAndDoesNotCacheTheCut(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k", "v"}, rows: wideRows(50, 100)}}
	e := &Endpoint{Name: "clicks", Path: "/v1/clicks", SQL: "select k, v from t where w = {{ tenant }}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	// THE LIMIT GOES IN BEFORE New, as the file's does: a handler reads its
	// limits when it is built.
	s, err := New(&Config{Name: "x", Env: "local", Dialect: postgres.Dialect{}, MaxRows: 100,
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Limits:    Limits{MaxResponseBytes: 2048},
		Cache:     &CacheConfig{Type: "redis", AddrFrom: "UNUSED", TTL: time.Hour},
		CacheKV:   &memoryKV{data: map[string][]byte{}},
		Endpoints: []*Endpoint{e}})
	if err != nil {
		t.Fatal(err)
	}
	rec := get(s, "/v1/clicks")
	var a answer
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &a) != nil || !a.Truncated || rec.Body.Len() > 2048 {
		t.Fatalf("%d, %d bytes, truncated=%v", rec.Code, rec.Body.Len(), a.Truncated)
	}
	if got := get(s, "/v1/clicks").Header().Get("X-Brevis-Cache"); got == "hit" {
		t.Error("an answer cut by bytes was served from the cache")
	}
}

func TestMaxResponseBytesInTheFile(t *testing.T) {
	sqlFile := map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"}
	cfg, err := fixture(t, good, sqlFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxResponseBytes != 8<<20 {
		t.Errorf("the default is %s", cfg.Limits.MaxResponseBytes)
	}
	cfg, err = fixture(t, strings.Replace(good, "listen:", "limits: {max_response_bytes: 1MiB}\nlisten:", 1)+
		"    limits: {max_response_bytes: 64KiB}\n", sqlFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints[1].maxResponse != 64<<10 {
		t.Errorf("the endpoint's ceiling is %s", cfg.Endpoints[1].maxResponse)
	}
	for name, c := range map[string]struct{ file, says string }{
		"an endpoint raising it": {strings.Replace(good, "listen:", "limits: {max_response_bytes: 1MiB}\nlisten:", 1) +
			"    limits: {max_response_bytes: 2MiB}\n", "lower"},
		"over the hard ceiling": {strings.Replace(good, "listen:", "limits: {max_response_bytes: 1GiB}\nlisten:", 1), "max_response_bytes"},
		"under a kilobyte":      {strings.Replace(good, "listen:", "limits: {max_response_bytes: 100}\nlisten:", 1), "max_response_bytes"},
	} {
		if _, err := fixture(t, c.file, sqlFile); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
