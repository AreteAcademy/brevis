package bigquery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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

	// scope is read AND write, and it stays that way. MEASURED 2026-10-09
	// against the API's own discovery document, which is the only thing that
	// can answer this:
	//
	//	curl .../discovery/v1/apis/bigquery/v2/rest
	//
	// CHECKPOINT B's F1 left one question open -- "narrowing the OAuth scope
	// to bigquery.readonly; that scope may not grant bigquery.jobs.create,
	// and a query is a job". The answer is stronger than "may not":
	// `https://www.googleapis.com/auth/bigquery.readonly` is listed on NO
	// method of this API at all. Not jobs.query, not jobs.getQueryResults,
	// not tables.list. Narrowing to it would not restrict this tool; it
	// would stop it.
	//
	// `cloud-platform.read-only` IS listed on jobs.query, jobs.getQueryResults
	// and tables.list, and is NOT listed on jobs.insert or tables.insert --
	// so it is the shape somebody would reach for. It is not used here for a
	// reason this file cannot fix: ONE PACKAGE SERVES TWO VERBS. `build`
	// creates tables and views through this same conn; `serve` only reads.
	// A single constant cannot be narrow for one and wide for the other, and
	// whether that scope actually refuses DDL submitted through jobs.query
	// is NOT measured -- it needs a credential restricted to it, which is a
	// thing to issue deliberately rather than to assume.
	//
	// WHAT ACTUALLY DEFENDS THIS IS NOT THE SCOPE. `dialect.WriteProbe`
	// asks the warehouse, once per connection, whether this credential could
	// create a table, and `serve` refuses to answer anything on one that
	// can. That is a measurement of what the credential may do, whatever
	// scope it was issued under.
	scope = "https://www.googleapis.com/auth/bigquery"

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

	// What it ACTUALLY cost, beside what the dry run said it would. An
	// unreadable figure is left at zero: "the warehouse did not say" is an
	// answer, and inventing one would hide the gap worth seeing.
	out.Scanned, _ = strconv.ParseInt(res.TotalBytesProcessed, 10, 64)

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

// CanWrite asks whether this credential may create a table, by dry-running
// one. See dialect.WriteProbe.
//
// A DRY RUN OF A WRITE CREATES NOTHING AND COSTS NOTHING, which is what makes
// this affordable as a boot check rather than a thing somebody audits twice a
// year. The name carries a nonce so it can never collide with a real table
// and turn "already exists" into the answer.
func (c *conn) CanWrite(ctx context.Context, schema string) (bool, error) {
	stmt := fmt.Sprintf("CREATE TABLE %s.brevis_write_probe_%d (probe INT64)",
		schema, time.Now().UnixNano())
	if _, err := c.runWith(ctx, stmt, map[string]any{"dryRun": true}); err != nil {
		// NOT A NO, just not a yes. See WriteProbe.CanWrite.
		return false, err
	}
	return true, nil
}

// Relations lists everything a SELECT could name. See dialect.Lister.
//
// TWO QUERIES AND THE REGION IS ASKED FOR, not configured. BigQuery refuses
// `INFORMATION_SCHEMA.TABLES` without a dataset or a region qualifier -- and
// a region declared in a connection file would be a second statement of a
// fact the warehouse already holds, wrong the day somebody adds a dataset
// elsewhere.
//
// So: SCHEMATA at the project level, which works unqualified and CARRIES the
// location of each dataset; then one TABLES query per distinct location.
// Most projects are one region, which makes this two round trips.
//
// Measured on 2026-10-09: 10 MiB each, the floor BigQuery bills for any
// metadata query, and a whole region costs the same as one dataset.
func (c *conn) Relations(ctx context.Context) ([]dialect.Relation, error) {
	schemata, err := c.runWith(ctx, "SELECT schema_name, location FROM INFORMATION_SCHEMA.SCHEMATA", nil)
	if err != nil {
		return nil, err
	}
	regions := map[string]bool{}
	for _, r := range schemata.Rows {
		if len(r.F) > 1 {
			if loc, ok := r.F[1].V.(string); ok && loc != "" {
				regions[loc] = true
			}
		}
	}
	// A project with no dataset at all is an answer, not a failure.
	if len(regions) == 0 {
		return nil, nil
	}

	var out []dialect.Relation
	for _, loc := range sorted(regions) {
		// The region is a LOCATION BigQuery just told us, and it is written
		// into a backticked name. It can hold a dash (`europe-west4`) and
		// nothing else: anything that is not a region this refuses rather
		// than quotes, because the alternative is interpolating a server's
		// answer into SQL.
		if !region.MatchString(loc) {
			return nil, fmt.Errorf("bigquery named a dataset location this will not put in a query")
		}
		res, err := c.runWith(ctx,
			fmt.Sprintf("SELECT table_schema, table_name FROM `region-%s`.INFORMATION_SCHEMA.TABLES", loc), nil)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			if len(r.F) < 2 {
				continue
			}
			schema, _ := r.F[0].V.(string)
			name, _ := r.F[1].V.(string)
			out = append(out, dialect.Relation{Schema: schema, Name: name})
		}
	}
	return out, nil
}

// Columns says what one relation holds. See dialect.Describer.
//
// PER DATASET, which is where BigQuery keeps this view: the project-wide
// shape would be `region-X`.INFORMATION_SCHEMA.COLUMNS, and that returns
// every field of every table in a region -- the one metadata answer that is
// genuinely large, and the reason CHECKPOINT D made columns lazy.
//
// THE NAMES ARE REFUSED, NOT QUOTED, which is the rule Relations already
// follows for a region. There is no parameter here: a dataset is part of the
// table path and cannot be bound, so a name outside this shape is refused
// rather than interpolated. Both names come from a listing -- the
// warehouse's own answer -- but the REQUEST comes from a console, and a
// console is not a warehouse.
func (c *conn) Columns(ctx context.Context, r dialect.Relation) ([]dialect.Column, error) {
	if !named(r.Schema) || !named(r.Name) {
		return nil, fmt.Errorf("this will not put that relation's name in a query")
	}
	res, err := c.runWith(ctx, fmt.Sprintf(
		"SELECT column_name, data_type\n"+
			"  FROM %s.INFORMATION_SCHEMA.COLUMNS\n"+
			" WHERE table_name = '%s'\n"+
			" ORDER BY ordinal_position", r.Schema, r.Name), nil)
	if err != nil {
		return nil, err
	}
	var out []dialect.Column
	for _, row := range res.Rows {
		if len(row.F) < 2 {
			continue
		}
		name, _ := row.F[0].V.(string)
		typ, _ := row.F[1].V.(string)
		out = append(out, dialect.Column{Name: name, Type: typ})
	}
	return out, nil
}

// named is the rule the DDL already applies to every identifier it writes,
// reused here rather than restated: the pattern and the length limit are
// checked apart for the reason maxIdentifier gives.
func named(s string) bool {
	return identifier.MatchString(s) && len(s) <= maxIdentifier
}

// region is what a dataset location may look like: `US`, `EU`,
// `europe-west4`. See Relations for why this is a refusal and not a quote.
var region = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,30}[A-Za-z0-9]$`)

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
