package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

type queryRequest struct {
	// Target names the CONNECTION, and nothing else about it is used: the
	// statement names its own relations. A query is not confined to the
	// destination it was opened from -- the point of the tab is to join it
	// to the others.
	Target string `json:"target"`

	// Statement is the caller's SQL, run exactly as written if it is a read.
	Statement string `json:"statement"`

	Limit int `json:"limit"`
}

type queryResponse struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
	Limit     int      `json:"limit"`

	// Bytes is what the warehouse says it processed, and Millis how long it
	// took. BOTH ARE ON SCREEN under the grid: a query tab that does not
	// show what a query cost is one that teaches nobody anything about the
	// difference between the query they wrote and the one that was cheap.
	Bytes  int64 `json:"bytes"`
	Millis int64 `json:"ms"`
}

// query runs one statement the caller wrote.
//
// EVERY LIMIT IS ON THIS SIDE. The console is a convenience; this is the
// line, and the tests send what somebody with the token and curl would send.
// The order below is the design: refuse for free first, then spend.
func (s *Service) query(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	line := record{Event: "query", Outcome: "refused"}
	defer func() { s.audit(line, started) }()

	var req queryRequest
	// A statement can be long, and 64 KiB is longer than any query somebody
	// typed into a box. It is still a bound: without one, the body is the
	// one place a caller sets the size of what this allocates.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		line.Outcome = "malformed"
		refuse(w, http.StatusBadRequest, "the body is not a query request")
		return
	}
	table, err := ParseTarget(req.Target)
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	line.Connection = table.Connection

	// THE HASH, AND NEVER THE TEXT. Taken before anything is decided, so a
	// refused query is as traceable as one that ran.
	sum := sha256.Sum256([]byte(req.Statement))
	line.Statement = hex.EncodeToString(sum[:])[:16]

	// THE CLASSIFIER, BEFORE A CONNECTION EXISTS. A refusal that happened
	// after connecting is a refusal that spent a connection, and a warehouse
	// that saw a statement this service had already decided against.
	if err := ReadOnly(table.Dialect, req.Statement); err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}

	limit := req.Limit
	if limit <= 0 || limit > s.opt.Rows {
		limit = s.opt.Rows
	}
	line.Rows = limit

	// A SLOT BEFORE A CONNECTION, because the connection is the scarce thing
	// and holding one while waiting for a slot is holding it for nothing.
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		line.Outcome = "busy"
		refuse(w, http.StatusServiceUnavailable,
			"this service is already running as many queries as it will run at once")
		return
	}

	// THE CLOCK STARTS HERE and not at the request: a query that waited for
	// a slot did not take that long to run. It bounds the dry run and the
	// read together, which is what somebody means by "a query may take 30s".
	ctx, cancel := context.WithTimeout(r.Context(), s.opt.Timeout)
	defer cancel()

	conn, err := s.opt.Open(ctx, table)
	if err == nil && conn == nil {
		// NOTHING AND NO ERROR EITHER. No dialect does this; a registry of
		// connections is exactly the kind of code that returns a zero value
		// on a path nobody walked yet, and the next line would call Close on
		// a nil interface and take the handler down with a stack trace.
		err = errNoConnection
	}
	if err != nil {
		if errors.Is(err, ErrNoConnection) {
			line.Outcome = "undeclared"
			refuseWith(w, http.StatusBadRequest, CodeNoConnection, ErrNoConnection.Error())
			return
		}
		line.Outcome = "unreachable"
		refuse(w, http.StatusBadGateway, "the warehouse could not be reached")
		return
	}
	defer func() { _ = conn.Close(context.Background()) }()

	res, err := s.read(ctx, conn, table, req.Statement, limit)
	if err != nil {
		var no refusal
		if errors.As(err, &no) {
			line.Outcome = no.outcome
			refuse(w, no.code, no.why)
			return
		}
		line.Outcome = "failed"
		refuse(w, http.StatusBadGateway, "the warehouse refused this query")
		return
	}

	line.Outcome = "ok"
	line.Bytes = res.Scanned
	line.Returned = len(res.Rows)
	write(w, http.StatusOK, queryResponse{
		Columns: res.Columns, Rows: res.Rows, Truncated: res.Truncated,
		Limit: limit, Bytes: res.Scanned, Millis: time.Since(started).Milliseconds(),
	})
}

// outcomeWritable is what an audit line says when the credential can write.
const outcomeWritable = "writable-credential"

// assertReadOnly refuses a connection whose credential is allowed to write.
//
// IT IS THE ANSWER TO THE ONE QUESTION THE CLASSIFIER CANNOT ANSWER. A
// function call writes whatever the function writes -- `SELECT nextval('s')`
// advances a sequence, and no classifier short of a planner sees inside one.
// What actually holds that line is the role, and this is what stops the role
// from being an assumption.
//
// LOCALLY IT IS SAID AND NOT REFUSED. On a laptop the credential is the
// developer's own account, which can write everything; refusing there would
// make the tool unusable and teach somebody to turn the check off, which is
// the same reasoning BREVIS_ENV already carries for the token. It is still
// said out loud, once, on the audit stream.
func (s *Service) assertReadOnly(ctx context.Context, conn dialect.Conn, table Table) error {
	probe, can := conn.(dialect.WriteProbe)
	if !can {
		// A warehouse that cannot be asked is not refused for a question it
		// was never asked. Postgres has no dry run and implements none of
		// this.
		return nil
	}
	s.mu.Lock()
	writes, asked := s.probed[table.Connection]
	s.mu.Unlock()

	if !asked {
		schema, _, _ := strings.Cut(table.Relation, ".")
		answer, err := probe.CanWrite(ctx, schema)

		// THREE OUTCOMES AND NOT TWO. "Could not check" is not "checked and
		// fine", and recording the second when the first happened is worse
		// than recording nothing: an operator reading `read-only` believes
		// the credential was examined and cleared.
		//
		// Found by RUNNING it, against a seeded catalog whose project has
		// BigQuery disabled -- the probe could not even reach a warehouse
		// and the line said the credential was read-only.
		switch {
		case err != nil:
			// AN ERROR IS NOT A YES, whatever came back beside it, and it is
			// not remembered either: caching a failure would let one
			// transient error turn this check off for the life of the
			// process, and nobody notices when it is off.
			writes = false
			s.audit(record{Event: "credential", Connection: table.Connection,
				Outcome: "unprobed"}, time.Now())
		default:
			writes = answer
			outcome := "read-only"
			if writes {
				outcome = outcomeWritable
			}
			s.mu.Lock()
			s.probed[table.Connection] = writes
			s.mu.Unlock()
			s.audit(record{Event: "credential", Connection: table.Connection,
				Outcome: outcome}, time.Now())
		}
	}
	if !writes || s.opt.Env == EnvLocal {
		return nil
	}
	return refusal{http.StatusInternalServerError, outcomeWritable,
		"this service holds a warehouse credential that can CREATE TABLES, so it is " +
			"not read-only and nothing here is. Give it a role that cannot write -- on " +
			"BigQuery, roles/bigquery.dataViewer with roles/bigquery.jobUser -- or run " +
			"with BREVIS_ENV=local if this is a laptop"}
}

// refusal is an answer this service decided on, carrying the status and the
// sentence that go with it.
//
// A TYPE AND NOT A STRING, so `read` can be shared by the preview and the
// query without either of them guessing which failures are the warehouse's
// and which are this service's -- and so a refusal cannot be turned into a
// 502 that says nothing by a caller that forgot to look.
type refusal struct {
	code    int
	outcome string
	why     string
}

func (r refusal) Error() string { return r.why }

// read prices a statement, refuses it if it is too expensive, and runs it.
//
// ONE FUNCTION FOR BOTH ENDPOINTS. A preview is `SELECT * FROM t LIMIT n`
// and a query is the caller's own, but what happens to them from here is the
// same thing and must stay the same thing: the preview was already costing
// what it cost before anybody priced it.
func (s *Service) read(ctx context.Context, conn dialect.Conn, table Table, statement string, limit int) (dialect.Result, error) {
	reader, can := conn.(dialect.Reader)
	if !can {
		return dialect.Result{}, refusal{http.StatusNotImplemented, "unsupported",
			"this warehouse cannot return a result set"}
	}
	// BEFORE ANYTHING IS PRICED OR RUN, and on BOTH endpoints -- which is
	// half of why `read` is one function. A credential that can write makes
	// every other limit on this surface decorative, so it is checked before
	// any of them are applied.
	if err := s.assertReadOnly(ctx, conn, table); err != nil {
		return dialect.Result{}, err
	}

	// THE DRY RUN, where there is one. A warehouse that charges for a
	// machine by the hour has nothing to say here and is not refused for
	// staying quiet -- its limits are the row ceiling and the clock.
	if priced, can := conn.(dialect.Estimator); can {
		scan, err := priced.Estimate(ctx, statement)
		if err != nil {
			// THE WAREHOUSE'S OWN WORDS, and only here. A dry run fails on a
			// syntax error or a missing table, which is the one thing the
			// person who typed the SQL needs back; everything else this
			// service says about a warehouse is deliberately vague.
			return dialect.Result{}, refusal{http.StatusBadRequest, "unpriced",
				"the warehouse would not run this: " + trim(err.Error())}
		}
		if scan > s.opt.Bytes {
			return dialect.Result{}, refusal{http.StatusBadRequest, "too-expensive", fmt.Sprintf(
				"this query would scan %s, and this service stops at %s. Narrow the columns, "+
					"or add a filter on a partitioned one.", BytesText(scan), BytesText(s.opt.Bytes))}
		}
	}

	// ONE MORE ROW THAN WILL BE SHOWN. See the preview: a LIMIT inside the
	// statement makes the warehouse's own count agree with what came back,
	// so the extra row is the only thing that can tell a cut from a table
	// that ends there.
	probe := limit + 1
	res, err := reader.Read(ctx, dialect.Request{Query: statement, Limit: probe, MaxBytes: s.opt.Bytes})
	if err != nil {
		return dialect.Result{}, err
	}
	res.Truncated = len(res.Rows) > limit
	if res.Truncated {
		res.Rows = res.Rows[:limit]
	}
	if res.Rows == nil {
		res.Rows = [][]any{}
	}
	if res.Columns == nil {
		res.Columns = []string{}
	}
	return res, nil
}

// trim bounds a warehouse's message. A driver can return a page.
func trim(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// record is one audit line.
//
// NO SQL TEXT, which is #65's own rule: a WHERE clause carries customer data
// and an audit file outlives the question it was written for. The hash is
// what an audit actually needs -- "this ran forty times today" is an answer,
// and it is the one that survives dropping the text.
type record struct {
	At         string `json:"at"`
	Event      string `json:"event"`
	Connection string `json:"connection,omitempty"`
	Statement  string `json:"statement,omitempty"`
	Outcome    string `json:"outcome"`

	// Rows is the ceiling this ran under, Returned how many came back, Bytes
	// what it scanned. All three are zero on a query that never ran, which
	// is the difference worth being able to see.
	Rows     int   `json:"rows,omitempty"`
	Returned int   `json:"returned"`
	Bytes    int64 `json:"bytes"`
	Millis   int64 `json:"ms"`
}

// audit puts one line out. ONE PER QUERY, whatever happened to it: an audit
// that records only what succeeded cannot answer the question it is read for.
func (s *Service) audit(line record, started time.Time) {
	if s.opt.Audit == nil {
		return
	}
	// STAMPED HERE AND NOWHERE ELSE, so every line carries one: the
	// credential line forgot its own when each caller set its own stamp.
	line.At = started.UTC().Format(time.RFC3339)
	line.Millis = time.Since(started).Milliseconds()
	// THE SAME RECORD THE AUDIT LINE CARRIES, so the two cannot disagree
	// about what happened -- minus the hash, which a label must never hold.
	if line.Event == "query" || line.Event == "preview" {
		s.met.observe(line.Event, line)
	}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	_, _ = s.opt.Audit.Write(append(b, '\n'))
}
