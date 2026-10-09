package bigquery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// THE REST ENDPOINT AND NOT THE OFFICIAL CLIENT, and the reason is measured
// rather than felt. `cloud.google.com/go/bigquery` in an empty main:
//
//	official client   31 MB   526 packages   236 modules
//	this file         8.5 MB  201 packages     3 modules
//
// S8's image budget is 30 MB, so the official client does not fit before a
// line of brevis-sql is written. What this tool needs from BigQuery is two
// operations -- run a statement, read one value -- and the client's
// connection pooling, streaming inserts, schema types and load jobs are
// weight nothing here would call.
//
// WHAT IT COSTS, said plainly: job polling, location and the 200-carrying-
// errors case are handled in this file instead of in Google's, and each one
// has a test because each one is a way this could be quietly wrong.
const (
	endpoint = "https://bigquery.googleapis.com/bigquery/v2"
	scope    = "https://www.googleapis.com/auth/bigquery"

	// jobTimeout is how long the server may hold a request before answering
	// "not finished". It is not a limit on the QUERY -- a CREATE TABLE AS
	// over a large source runs for minutes and is polled below.
	jobTimeout = 30 * time.Second
)

// Open connects, with the project ID where other dialects take a DSN.
//
// Credentials come from Application Default Credentials and from nowhere
// else: on a workload identity that is the pod's own identity, which is what
// the gateway's BigQuery sink already relies on. A key file in a project
// directory is the thing this avoids.
func (Dialect) Open(ctx context.Context, project string) (dialect.Conn, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("bigquery needs a project id where other dialects " +
			"take a connection string: --dsn-from names the variable holding it")
	}
	if strings.ContainsAny(project, "/ \t") {
		return nil, fmt.Errorf("bigquery: %q is not a project id", project)
	}

	ts, err := google.DefaultTokenSource(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("bigquery: no application default credentials "+
			"(`gcloud auth application-default login`, or a workload identity): %w", err)
	}
	return &conn{
		project: project,
		base:    endpoint,
		http:    oauth2.NewClient(ctx, ts),
		poll:    time.Second,
	}, nil
}

type conn struct {
	project string
	base    string
	http    *http.Client
	poll    time.Duration
}

// queryResponse is the shape of both jobs.query and queries.get.
type queryResponse struct {
	JobComplete  bool `json:"jobComplete"`
	JobReference struct {
		JobID    string `json:"jobId"`
		Location string `json:"location"`
	} `json:"jobReference"`
	Rows []struct {
		F []struct {
			V any `json:"v"`
		} `json:"f"`
	} `json:"rows"`
	// Schema carries the COLUMN NAMES, which nothing needed until a result
	// set had to be drawn: a grid with the right rows under the wrong
	// headings is worse than no grid.
	Schema struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	} `json:"schema"`
	// TotalRows is what the query MATCHED, as a string -- BigQuery sends its
	// 64-bit counts as JSON strings. It is how `Read` knows the limit cut
	// something, without a second query.
	TotalRows string `json:"totalRows"`
	// TotalBytesProcessed is what the query WOULD scan, as a string for the
	// same reason. On a dry run it is the whole answer.
	TotalBytesProcessed string `json:"totalBytesProcessed"`
	// Errors is a job that RAN AND FAILED, and it arrives inside an HTTP
	// 200. A client reading only the status code reports every failed
	// CREATE as a success.
	Errors []struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"errors"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *conn) Exec(ctx context.Context, statement string) error {
	_, err := c.run(ctx, statement)
	return err
}

// Scalar reads one value, and NO ROW IS AN ANSWER -- "nothing of that name
// is there" is what KindOf returns on every model's first build.
func (c *conn) Scalar(ctx context.Context, query string) (any, error) {
	res, err := c.run(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 || len(res.Rows[0].F) == 0 {
		return nil, nil
	}
	return res.Rows[0].F[0].V, nil
}

// Read runs a query and returns at most `req.Limit` rows. See dialect.Reader.
//
// `maxResults` ON THE REQUEST, not a LIMIT wrapped around the query. The
// caller's SQL is run exactly as written -- a wrapper would change what the
// warehouse plans, and it would be this package quietly editing a statement
// somebody is about to be charged for. The API caps what comes BACK.
//
// `maximumBytesBilled` is the OTHER limit, and it bounds what the query
// scans. It is the belt behind Estimate's brake: a query priced a moment ago
// can grow before it runs -- a table loaded in between, a view over
// something that changed -- and this is what makes the refusal hold anyway.
// BigQuery takes it as a string, because its int64 fields travel as strings
// in JSON, and sending a number is sending a parameter the server ignores.
func (c *conn) Read(ctx context.Context, req dialect.Request) (dialect.Result, error) {
	extra := map[string]any{"maxResults": req.Limit}
	// NO CEILING MEANS NO FIELD. BigQuery reads `maximumBytesBilled: 0` as
	// "bill nothing", which refuses every query there is.
	if req.MaxBytes > 0 {
		extra["maximumBytesBilled"] = strconv.FormatInt(req.MaxBytes, 10)
	}
	res, err := c.runWith(ctx, req.Query, extra)
	if err != nil {
		return dialect.Result{}, err
	}

	out := dialect.Result{Columns: make([]string, 0, len(res.Schema.Fields))}
	for _, f := range res.Schema.Fields {
		out.Columns = append(out.Columns, f.Name)
	}
	for _, r := range res.Rows {
		row := make([]any, 0, len(r.F))
		for _, cell := range r.F {
			row = append(row, cell.V)
		}
		out.Rows = append(out.Rows, row)
	}

	// A total the response did not carry is not truncation. Parsing it as
	// zero and comparing would report "not truncated" for a query that was,
	// which is the silence Truncated exists to break -- so an unreadable
	// total falls back to what is visible instead.
	if total, err := strconv.ParseInt(res.TotalRows, 10, 64); err == nil {
		out.Truncated = total > int64(len(out.Rows))
	}
	return out, nil
}

// Estimate prices a query without running it. See dialect.Estimator.
//
// A DRY RUN COSTS NOTHING AND IS THE ONLY LIMIT THAT WORKS BEFORE THE MONEY
// IS SPENT. `totalBytesProcessed` comes back with nothing billed -- measured
// against real BigQuery on 2026-10-09, rather than read in a document.
//
// It is not polled, and does not need to be: a dry run creates no job, so
// the answer is in the first response or there is no answer.
func (c *conn) Estimate(ctx context.Context, query string) (int64, error) {
	res, err := c.runWith(ctx, query, map[string]any{"dryRun": true})
	if err != nil {
		return 0, err
	}
	// AN ERROR AND NEVER A ZERO. Zero is under every ceiling, so a total
	// this could not read would wave through the one query nobody measured.
	scanned, err := strconv.ParseInt(res.TotalBytesProcessed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bigquery priced the query with something that is not a number of bytes (%q)",
			truncateTo(res.TotalBytesProcessed, 40))
	}
	return scanned, nil
}

// Target is `bigquery://project/dataset/table`.
//
// The project is what Open was given, so there is nothing to parse and
// nothing of a credential to leak -- a BigQuery connection has no DSN.
func (c *conn) Target(ref string) string {
	dataset, table, _ := strings.Cut(ref, ".")
	return "bigquery://" + c.project + "/" + dataset + "/" + table
}

func (c *conn) Close(context.Context) error { return nil }

// run posts the statement and waits for the job, however long it takes.
func (c *conn) run(ctx context.Context, statement string) (*queryResponse, error) {
	return c.runWith(ctx, statement, nil)
}

// runWith is run, plus whatever the caller needs on the request body.
//
// EXTRA AND NEVER OVERRIDE: the three fields below are the contract every
// statement this package sends depends on, and a caller that could replace
// `useLegacySql` would get a parse error about SQL it did not write. The
// merge happens after them and a key they already hold is kept.
func (c *conn) runWith(ctx context.Context, statement string, extra map[string]any) (*queryResponse, error) {
	req := map[string]any{
		"query": statement,
		// STANDARD SQL. The default on this endpoint is legacy, which has no
		// CREATE OR REPLACE at all, so every statement this tool writes
		// would be rejected for a reason that reads like a syntax error.
		"useLegacySql": false,
		"timeoutMs":    jobTimeout.Milliseconds(),
	}
	for k, v := range extra {
		if _, taken := req[k]; !taken {
			req[k] = v
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// `/queries` and not `/jobs/query`. `jobs.query` is the METHOD's name in
	// Google's reference; the HTTP path is this, and the other one answers a
	// 404 as an HTML page -- which is why call() reports a non-JSON body
	// with what arrived instead of "invalid character '<'".
	res, err := c.call(ctx, http.MethodPost,
		fmt.Sprintf("%s/projects/%s/queries", c.base, url.PathEscape(c.project)),
		body, statement)
	if err != nil {
		return nil, err
	}

	for !res.JobComplete {
		// THE LOCATION TRAVELS. A job created in the EU and asked for
		// without one is "not found", which reads as a bug in this tool
		// rather than as a missing parameter.
		q := url.Values{
			"timeoutMs": {fmt.Sprint(jobTimeout.Milliseconds())},
			"location":  {res.JobReference.Location},
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for the job: %w", ctx.Err())
		case <-time.After(c.poll):
		}
		res, err = c.call(ctx, http.MethodGet,
			fmt.Sprintf("%s/projects/%s/queries/%s?%s",
				c.base, url.PathEscape(c.project),
				url.PathEscape(res.JobReference.JobID), q.Encode()),
			nil, statement)
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (c *conn) call(ctx context.Context, method, u string, body []byte, statement string) (*queryResponse, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bigquery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var out queryResponse
	// A body that is not JSON is reported with what arrived: a proxy's HTML
	// error page is a different problem from a query BigQuery refused, and
	// "invalid character '<'" alone does not say which happened.
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bigquery answered %d with something that is not JSON: %s",
			resp.StatusCode, truncate(raw))
	}
	if resp.StatusCode/100 != 2 {
		if out.Error != nil {
			return nil, fmt.Errorf("bigquery: %s", out.Error.Message)
		}
		return nil, fmt.Errorf("bigquery answered %d: %s", resp.StatusCode, truncate(raw))
	}
	// 200 AND FAILED. See queryResponse.Errors.
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("bigquery refused it (%s): %s",
			out.Errors[0].Reason, out.Errors[0].Message)
	}
	return &out, nil
}

func truncate(b []byte) string { return truncateTo(string(b), 200) }

func truncateTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
