package endpointcall

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTheAddressListIsReadByAPI(t *testing.T) {
	c, err := New("example-api=http://endpoints:8080, metrics=https://api.acme.example", "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Configured("example-api") || !c.Configured("metrics") || c.Configured("other") {
		t.Error("Configured is wrong")
	}
	if none, err := New("", ""); err != nil || none.Configured("example-api") {
		t.Errorf("an empty list: %v", err)
	}
	for _, bad := range []string{"example-api", "=http://x", "a=ftp://x", "a=http://u:pw@x", "a=http://x,a=http://y"} {
		if _, err := New(bad, ""); err == nil {
			t.Errorf("%q was accepted", bad)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("%q: the error repeats a credential", bad)
		}
	}
}

// THE CALL IS THE CALLER'S: the declared path, the parameters as given, the
// console's key as a bearer -- and the answer as the API said it, status,
// cache and body, the rows in the order the query named them.
func TestACallForwardsTheParametersAndTheKeyAndReadsTheAnswer(t *testing.T) {
	var got *http.Request
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("X-Brevis-Cache", "hit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"day":"2026-10-12","sku":"W-1","units":3},{"day":"2026-10-13","sku":"W-2","units":null}],` +
			`"truncated":true,"meta":{"endpoint":"orders","rows":2}}`))
	}))
	defer api.Close()
	c, _ := New("example-api="+api.URL, "console-key")

	a, err := c.Call(context.Background(), "example-api", "/v1/orders", url.Values{"period": {"today"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/v1/orders" || got.URL.Query().Get("period") != "today" ||
		got.Header.Get("Authorization") != "Bearer console-key" {
		t.Errorf("asked %s %v", got.URL, got.Header)
	}
	if a.Status != 200 || a.Cache != "hit" || !a.Truncated || strings.Join(a.Columns, ",") != "day,sku,units" ||
		len(a.Rows) != 2 || a.Rows[1][2] != nil || a.Rows[0][1] != "W-1" {
		t.Errorf("answer %+v", a)
	}
}

// A REFUSAL IS AN ANSWER, in the API's own words: they were chosen to name
// the parameter and never quote the SQL, and the console repeats them.
func TestARefusalCarriesTheAPIsWords(t *testing.T) {
	var auth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"the parameter period is one of: today, yesterday"}`))
	}))
	defer api.Close()
	c, _ := New("a="+api.URL, "")
	a, err := c.Call(context.Background(), "a", "/v1/x", url.Values{"period": {"nope"}})
	if err != nil || a.Status != 400 || a.Error != "the parameter period is one of: today, yesterday" {
		t.Errorf("%+v %v", a, err)
	}
	// and no key configured is no header, which only a local API accepts
	if auth != "" {
		t.Errorf("Authorization %q was sent with no key configured", auth)
	}
}

func TestAnAPINobodyConfiguredIsAnError(t *testing.T) {
	c, _ := New("", "")
	if _, err := c.Call(context.Background(), "nope", "/v1/x", nil); err != ErrNotConfigured {
		t.Errorf("got %v", err)
	}
}

func TestAnUnreachableAPISaysSoWithoutItsAddress(t *testing.T) {
	c, _ := New("a=http://127.0.0.1:1", "secret-key")
	_, err := c.Call(context.Background(), "a", "/v1/x", nil)
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "secret") {
		t.Errorf("got %v", err)
	}
}

// A BODY PAST THE CEILING IS REFUSED rather than held: the API's own
// ceiling is 64 MiB, and a console page is not where 64 MiB goes.
func TestAnAnswerPastTheConsolesCeilingIsRefused(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"v":"` + strings.Repeat("x", maxBody) + `"}]}`))
	}))
	defer api.Close()
	c, _ := New("a="+api.URL, "")
	if _, err := c.Call(context.Background(), "a", "/v1/x", nil); err == nil {
		t.Error("an answer past the ceiling was read")
	}
}

// A NESTED VALUE IS JSON, not Go's own syntax. A jsonb or STRUCT column
// arrives as an object, and decoded into `any` it is a map -- which, drawn
// with fmt.Sprint, is `map[clicks:3]`: a format nobody who called the API
// would recognise as what it sent.
func TestANestedCellIsKeptAsJSON(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"k":1,"o":{"clicks":3,"tags":["a","b"]},"l":[1,{"x":null}]}]}`))
	}))
	defer api.Close()
	c, _ := New("a="+api.URL, "")
	a, err := c.Call(context.Background(), "a", "/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Rows[0][1] != `{"clicks":3,"tags":["a","b"]}` || a.Rows[0][2] != `[1,{"x":null}]` || a.Rows[0][0] != "1" {
		t.Errorf("row %#v", a.Rows[0])
	}
}

// THE CONSOLE DRAWS AT MOST maxRows, and counts the rest. Found in review:
// 4 MiB of narrow rows is over 400 000 of them, a page of tens of megabytes
// rendered in the engine and held by a browser tab. The preview tab draws
// twenty for the same reason.
func TestAtMostMaxRowsAreKeptAndTheRestCounted(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := make([]string, maxRows+100)
		for i := range rows {
			rows[i] = `{"k":1}`
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(rows, ",") + `]}`))
	}))
	defer api.Close()
	c, _ := New("a="+api.URL, "")
	a, err := c.Call(context.Background(), "a", "/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Rows) != maxRows || a.Received != maxRows+100 {
		t.Errorf("kept %d, received %d", len(a.Rows), a.Received)
	}
}

// NO REDIRECT IS FOLLOWED. brevis-sql-api never redirects, so one is
// something in front of it -- and Go re-sends the Authorization header to
// a redirect on the same host, handing the console's key to a path nobody
// chose. The redirect is the answer, as a refusal.
func TestARedirectIsNotFollowed(t *testing.T) {
	followed := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed = true
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer api.Close()
	c, _ := New("a="+api.URL, "console-key")
	a, err := c.Call(context.Background(), "a", "/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if followed || a.Status != http.StatusFound {
		t.Errorf("followed=%v status=%d", followed, a.Status)
	}
}
