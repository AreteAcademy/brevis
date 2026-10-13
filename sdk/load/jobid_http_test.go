package load

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
	"google.golang.org/api/option"
)

// fakeBigQuery answers the handful of calls a named load job makes, with the
// real client on the other side: the bugs here live in how the client uses its
// request body, which no fake job interface can see.
type fakeBigQuery struct {
	mu      sync.Mutex
	jobs    map[string]string // job id -> the JSON status a lookup returns
	uploads map[string]string // job id -> the body its insert carried
	fresh   string            // the status a new job starts in; DONE when empty
}

func newFakeBigQuery() *fakeBigQuery {
	return &fakeBigQuery{jobs: map[string]string{}, uploads: map[string]string{}}
}

func (f *fakeBigQuery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
		id := between(string(body), `"jobId":"`, `"`)
		if _, taken := f.jobs[id]; taken {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":409,"message":"Already Exists: Job `+id+`","errors":[{"reason":"duplicate"}]}}`)
			return
		}
		status := f.fresh
		if status == "" {
			status = `{"state":"DONE"}`
		}
		f.uploads[id] = string(body)
		f.jobs[id] = status
		_, _ = io.WriteString(w, `{"jobReference":{"projectId":"p","jobId":"`+id+`","location":"US"},"status":`+status+`}`)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if _, ok := f.jobs[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Not found: Job `+id+`"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"jobReference":{"projectId":"p","jobId":"`+id+`","location":"US"},"status":`+f.jobs[id]+`}`)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/datasets/d"):
		_, _ = io.WriteString(w, `{"datasetReference":{"projectId":"p","datasetId":"d"},"location":"US"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":404,"message":"no route `+r.Method+` `+r.URL.Path+`"}}`)
	}
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i+len(from):]
	return s[:strings.Index(s, to)]
}

func loaderAgainst(t *testing.T, f *fakeBigQuery, cfg *core.LoadConfig) (*Loader, *bigquery.Table) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	bq, err := bigquery.NewClient(context.Background(), "p",
		option.WithEndpoint(srv.URL+"/bigquery/v2/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProjectID, cfg.Dataset, cfg.Table, cfg.Format = "p", "d", "t", "ndjson"
	return &Loader{cfg: cfg, bq: bq}, bq.Dataset("d").Table("t")
}

// The review of #85 found it: the inline load hands the client ONE reader, the
// client drains it into its upload buffer on the first insert, and a retry
// moving to the next generation inserted an EMPTY load -- which BigQuery can
// take as a success with nothing written, and nothing buried either.
func TestTheNextGenerationUploadsTheRows(t *testing.T) {
	f := newFakeBigQuery()
	l, table := loaderAgainst(t, f, &core.LoadConfig{BatchKey: "batch-1"})
	data := []byte(`{"id":"first"}` + "\n" + `{"id":"second"}` + "\n")

	// An earlier attempt's job exists and failed: this load must take g2.
	base := l.jobBaseFor(table)
	f.jobs[base+"_g1"] = `{"state":"DONE","errorResult":{"reason":"backendError","message":"try again"}}`

	if _, _, _, err := l.loadInline(context.Background(), table, data); err != nil {
		t.Fatalf("load: %v", err)
	}
	body, ok := f.uploads[base+"_g2"]
	if !ok {
		t.Fatalf("g2 was never inserted; inserted: %v", f.uploads)
	}
	if !strings.Contains(body, "first") || !strings.Contains(body, "second") {
		t.Errorf("g2 was inserted without the rows -- an empty load:\n%s", body)
	}
}

// The job is named by the BATCH, not by its bytes. auto_table stamps
// brevis_received_at when it writes, so a retry a minute later carries other
// bytes -- and under a name made of the bytes it started a second job, which
// is #85 on the very path it was reported on.
func TestARetryWithOtherBytesAdoptsTheBatchJob(t *testing.T) {
	f := newFakeBigQuery()
	l, table := loaderAgainst(t, f, &core.LoadConfig{BatchKey: "batch-1"})

	for _, at := range []string{"00:06:24", "00:07:23"} {
		data := []byte(`{"id":"first","brevis_received_at":"2026-10-13T` + at + `Z"}` + "\n")
		if _, _, _, err := l.loadInline(context.Background(), table, data); err != nil {
			t.Fatalf("load at %s: %v", at, err)
		}
	}
	if len(f.uploads) != 1 {
		t.Errorf("one batch, %d jobs started: %v", len(f.uploads), keys(f.uploads))
	}
}

// Without a key there is no batch to name, and two loads are two writes --
// even of identical bytes. Naming jobs by content made a reload of the same
// data into a recreated table a "success" that wrote nothing.
func TestWithoutAKeyIdenticalLoadsAreTwoWrites(t *testing.T) {
	f := newFakeBigQuery()
	l, table := loaderAgainst(t, f, &core.LoadConfig{})
	data := []byte(`{"id":"first"}` + "\n")

	for i := 0; i < 2; i++ {
		if _, _, _, err := l.loadInline(context.Background(), table, data); err != nil {
			t.Fatalf("load %d: %v", i+1, err)
		}
	}
	if len(f.uploads) != 2 {
		t.Errorf("two loads, %d jobs: %v", len(f.uploads), keys(f.uploads))
	}
}

// An anonymous job can outlive the wait as well, and the caller still needs
// its name to ask what became of it.
func TestAnAnonymousJobIsNamedWhenTheWaitEnds(t *testing.T) {
	f := newFakeBigQuery()
	f.fresh = `{"state":"RUNNING"}`
	l, table := loaderAgainst(t, f, &core.LoadConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, _, err := l.loadInline(ctx, table, []byte(`{"id":"first"}`+"\n"))

	var je *core.LoadJobError
	if !errors.As(err, &je) || je.JobID == "" || je.State != core.JobRunning {
		t.Fatalf("err = %v (%+v)", err, je)
	}
	if _, ok := f.uploads[je.JobID]; !ok {
		t.Errorf("the error names %s, which is not the job that was started (%v)", je.JobID, keys(f.uploads))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A job that does not exist is an answer, not a failure to get one: the write
// it belonged to never landed.
func TestAJobThatDoesNotExistIsAbsent(t *testing.T) {
	l, _ := loaderAgainst(t, newFakeBigQuery(), &core.LoadConfig{})
	report, err := l.Job(context.Background(), "brevis_never_g1", "US")
	if err != nil {
		t.Fatalf("a missing job is an error: %v", err)
	}
	if report.State != core.JobAbsent {
		t.Errorf("report = %+v, want absent", report)
	}
}
