package load

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
		f.uploads[id] = string(body)
		f.jobs[id] = `{"state":"DONE"}`
		_, _ = io.WriteString(w, `{"jobReference":{"projectId":"p","jobId":"`+id+`","location":"US"},"status":{"state":"DONE"}}`)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
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
	f := &fakeBigQuery{jobs: map[string]string{}, uploads: map[string]string{}}
	l, table := loaderAgainst(t, f, &core.LoadConfig{})
	data := []byte(`{"id":"row-1"}` + "\n" + `{"id":"row-2"}` + "\n")

	// An earlier attempt's job exists and failed: this load must take g2.
	base := l.jobBaseFor(table, data)
	f.jobs[base+"_g1"] = `{"state":"DONE","errorResult":{"reason":"backendError","message":"try again"}}`

	if _, _, _, err := l.loadInline(context.Background(), table, data); err != nil {
		t.Fatalf("load: %v", err)
	}
	body, ok := f.uploads[base+"_g2"]
	if !ok {
		t.Fatalf("g2 was never inserted; inserted: %v", f.uploads)
	}
	if !strings.Contains(body, "row-1") || !strings.Contains(body, "row-2") {
		t.Errorf("g2 was inserted without the rows -- an empty load:\n%s", body)
	}
}
