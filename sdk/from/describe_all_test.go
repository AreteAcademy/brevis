package from_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/from/mysql"
	"github.com/AreteAcademy/brevis/sdk/from/postgres"
)

// A credential planted everywhere a source carries one: in the URL's user and
// password, in its query string, in a header, in a DSN.
const (
	secretURL = "https://loader:SEKRET@api.example.com/v1/items?api_key=SEKRET&page=2#frag"
	pgDSN     = "postgres://loader:SEKRET@db.internal:5432/shop?password=SEKRET"
	mysqlDSN  = "loader:SEKRET@tcp(db.internal:3306)/shop"
)

func secretHTTP() from.HTTP {
	return from.HTTP{URL: secretURL, Header: map[string][]string{
		"Authorization": {"Bearer SEKRET"}, "X-Tenant": {"acme"}}}
}

func shippedReaders() map[string]sdk.Reader {
	return map[string]sdk.Reader{
		"from.HTTP":      secretHTTP(),
		"from.Files":     from.Files{Path: "gs://acme-raw/orders/", Format: sdk.FormatCSV, Delimiter: ';'},
		"from.Many":      from.Many{Sources: []sdk.Reader{secretHTTP(), from.Files{Path: "./in/"}}, Workers: 4},
		"postgres.Query": postgres.Query{DSN: pgDSN, SQL: "SELECT id, total FROM orders WHERE placed_at >= $1", Args: []any{"2026-10-01"}},
		"mysql.Query":    mysql.Query{DSN: mysqlDSN, SQL: "SELECT id FROM orders"},
	}
}

func settings(t *testing.T, r sdk.Reader) map[string]any {
	t.Helper()
	c, ok := r.(sdk.Configured)
	if !ok {
		t.Fatalf("%T does not implement sdk.Configured", r)
	}
	out := map[string]any{}
	for _, s := range c.Configuration() {
		if _, dup := out[s.Key]; dup {
			t.Fatalf("%T lists %q twice", r, s.Key)
		}
		out[s.Key] = s.Value
	}
	return out
}

func TestEveryShippedReaderIsConfigured(t *testing.T) {
	for name, r := range shippedReaders() {
		if _, ok := r.(sdk.Configured); !ok {
			t.Errorf("%s does not implement sdk.Configured", name)
		}
	}
}

// THE TEST THIS TASK EXISTS FOR. No user, no password, no key and no query
// VALUE from any source reaches the description.
func TestNoReaderDescribesItsCredentials(t *testing.T) {
	for name, r := range shippedReaders() {
		raw, err := json.Marshal(r.(sdk.Configured).Configuration())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, leak := range []string{"SEKRET", "loader", "page=2", "acme\"", "frag", "2026-10-01"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s described %q: %s", name, leak, raw)
			}
		}
	}
}

// An HTTP source shows where it goes and how, and only the NAMES of what it
// sends: a query parameter's or a header's value is where a key lives.
func TestAnHTTPSourceShowsWhereAndOnlyTheNames(t *testing.T) {
	got := settings(t, secretHTTP())
	want := map[string]any{
		"url":           "https://api.example.com/v1/items",
		"query_params":  []string{"api_key", "page"},
		"headers":       []string{"Authorization", "X-Tenant"},
		"method":        "GET",
		"timeout":       "30s",
		"total_timeout": "5m",
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

func TestAnHTTPSourceShowsItsPagination(t *testing.T) {
	got := settings(t, from.HTTP{URL: "https://api.example.com/v1/items",
		CursorKey: "next", DataKey: "data", MaxPages: 50, PageSize: 100})
	want := map[string]any{"cursor_key": "next", "data_key": "data", "max_pages": 50, "page_size": 100}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

// The query, whole -- decided with the owner, 2026-10-11. Its arguments are
// counted, not shown: they are data, and they change every run.
func TestADatabaseSourceShowsItsQueryWhole(t *testing.T) {
	got := settings(t, shippedReaders()["postgres.Query"])
	want := map[string]any{
		"host": "db.internal", "port": 5432, "database": "shop",
		"sql":  "SELECT id, total FROM orders WHERE placed_at >= $1",
		"args": 1,
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
	my := settings(t, shippedReaders()["mysql.Query"])
	if my["host"] != "db.internal" || my["database"] != "shop" || my["sql"] != "SELECT id FROM orders" {
		t.Errorf("mysql = %v", my)
	}
}

// Many is its sources, each described under its position.
func TestManyDescribesEachOfItsSources(t *testing.T) {
	got := settings(t, shippedReaders()["from.Many"])
	want := map[string]any{
		"workers":          4,
		"sources.1.driver": "from.HTTP",
		"sources.1.url":    "https://api.example.com/v1/items",
		"sources.2.driver": "from.Files",
		"sources.2.path":   "./in/",
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

func TestFilesShowsWhereAndHow(t *testing.T) {
	got := settings(t, shippedReaders()["from.Files"])
	want := map[string]any{"path": "gs://acme-raw/orders/", "format": "csv", "delimiter": ";"}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}
