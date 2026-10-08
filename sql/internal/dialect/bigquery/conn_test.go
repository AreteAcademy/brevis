package bigquery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A fake BigQuery, so every rule below is pinned without a credential.
type fake struct {
	t     *testing.T
	posts []map[string]any
	gets  []string
	reply func(n int) (int, string) // status, body -- n is the POST count
}

func (f *fake) serve() *httptest.Server {
	// POST and GET share `/projects/{p}/queries`, which is the real API's
	// shape: `jobs.query` and `jobs.getQueryResults` are method NAMES, and
	// both live under that path. The fake separates them the way the server
	// does -- by method -- so a wrong URL in conn.go reaches neither and
	// shows up as the 404 it would in production.
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/p/queries", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("the request body is not JSON: %v", err)
		}
		f.posts = append(f.posts, body)
		status, reply := f.reply(len(f.posts))
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, reply)
	})
	mux.HandleFunc("/projects/p/queries/", func(w http.ResponseWriter, r *http.Request) {
		f.gets = append(f.gets, r.URL.String())
		_, _ = fmt.Fprint(w, `{"jobComplete":true,"rows":[{"f":[{"v":"done"}]}]}`)
	})
	s := httptest.NewServer(mux)
	f.t.Cleanup(s.Close)
	return s
}

func dial(t *testing.T, f *fake) *conn {
	t.Helper()
	s := f.serve()
	return &conn{project: "p", base: s.URL, http: s.Client(), poll: time.Millisecond}
}

func ok(body string) func(int) (int, string) {
	return func(int) (int, string) { return 200, body }
}

// STANDARD SQL, NEVER LEGACY. Legacy SQL rejects every statement this tool
// writes -- `CREATE OR REPLACE` is not in it at all -- and the default on
// this endpoint is the one that would.
func TestEveryQueryAsksForStandardSQL(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true}`)}
	c := dial(t, f)

	if err := c.Exec(context.Background(), "CREATE SCHEMA IF NOT EXISTS d"); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 1 {
		t.Fatalf("%d requests", len(f.posts))
	}
	if legacy, set := f.posts[0]["useLegacySql"]; !set || legacy != false {
		t.Errorf("useLegacySql is %v (set: %v); legacy SQL has no CREATE OR REPLACE",
			legacy, set)
	}
	if f.posts[0]["query"] != "CREATE SCHEMA IF NOT EXISTS d" {
		t.Errorf("the statement did not travel: %v", f.posts[0]["query"])
	}
}

func TestScalarReadsTheFirstCell(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"rows":[{"f":[{"v":"view"}]}]}`)}
	got, err := dial(t, f).Scalar(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "view" {
		t.Errorf("%v", got)
	}
}

// NO ROW IS AN ANSWER, not a failure: it is what KindOf returns for every
// model's first build, so an error here would make a fresh project
// unbuildable.
func TestNoRowIsNotAnError(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"rows":[]}`)}
	got, err := dial(t, f).Scalar(context.Background(), "SELECT 1")
	if err != nil || got != nil {
		t.Errorf("got %v, %v -- wanted nothing and no error", got, err)
	}
}

// A JOB THAT HAS NOT FINISHED IS POLLED, and the poll carries the LOCATION.
// A job created in the EU and asked for without one is "not found", which
// reads as a bug in this tool rather than as a missing parameter.
func TestAnIncompleteJobIsPolledWithItsLocation(t *testing.T) {
	f := &fake{t: t, reply: ok(
		`{"jobComplete":false,"jobReference":{"jobId":"j-1","location":"EU"}}`)}
	got, err := dial(t, f).Scalar(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "done" {
		t.Errorf("the polled result was not read: %v", got)
	}
	if len(f.gets) != 1 {
		t.Fatalf("polled %d times", len(f.gets))
	}
	if !strings.Contains(f.gets[0], "j-1") {
		t.Errorf("polled the wrong job: %s", f.gets[0])
	}
	if !strings.Contains(f.gets[0], "location=EU") {
		t.Errorf("the poll carries no location, so a job outside the US is "+
			"'not found': %s", f.gets[0])
	}
}

// AN HTTP 200 CARRYING ERRORS IS A FAILURE. This endpoint answers 200 for a
// job that ran and failed, with the reason in the body -- so a client that
// trusts the status code reports every failed CREATE as a success.
func TestAFailedJobInsideA200IsAnError(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"errors":[
		{"reason":"invalidQuery","message":"Syntax error: Unexpected end of script"}]}`)}
	err := dial(t, f).Exec(context.Background(), "SELECT")
	if err == nil {
		t.Fatal("a job that ran and failed was reported as a success")
	}
	if !strings.Contains(err.Error(), "Syntax error") {
		t.Errorf("the refusal does not carry BigQuery's own message: %v", err)
	}
}

func TestAnHTTPErrorCarriesBigQuerysMessage(t *testing.T) {
	f := &fake{t: t, reply: func(int) (int, string) {
		return 404, `{"error":{"code":404,"message":"Not found: Dataset p:nope"}}`
	}}
	err := dial(t, f).Exec(context.Background(), "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "Not found: Dataset p:nope") {
		t.Errorf("%v", err)
	}
}

// A context that is done stops the polling rather than looping on a job
// nobody is waiting for any more.
func TestPollingStopsWhenTheContextIsDone(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":false,"jobReference":{"jobId":"j","location":"US"}}`)}
	c := dial(t, f)
	c.poll = time.Hour // so only the context can end this

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Scalar(ctx, "SELECT 1"); err == nil {
		t.Error("a cancelled context did not stop the wait")
	}
}
