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
	line.Caller = whoAsked(r.Context())
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
	//
	// UNLESS SOMEBODY NAMED THIS CONNECTION. The tokeniser is the first of
	// three layers and they stand down together: leaving this one up would
	// refuse `CREATE TABLE` on a connection whose whole point is that it may
	// create one, and the refusal would read as a bug rather than a rule.
	if !s.mayWrite(table.Connection) {
		if err := ReadOnly(table.Dialect, req.Statement); err != nil {
			refuse(w, http.StatusBadRequest, err.Error())
			return
		}
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

	res, err := s.read(ctx, conn, table, req.Statement, limit, &line)
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

// outcomeGuarded is what it says when the credential can write and the
// WAREHOUSE has been told to refuse writes anyway.
//
// A THIRD WORD AND NOT SILENCE. "read-only" would be a lie -- the credential
// is not -- and `writable-credential` would read as the refusal it no longer
// is. An operator has to be able to tell "this role cannot write" from "this
// role can, and something else is stopping it", because only the second one
// changes if that something is ever removed.
const outcomeGuarded = "writable-guarded"

// outcomeWrites is what it says when the WAREHOUSE read the statement as
// something that writes.
const outcomeWrites = "writes"

// notAQuery refuses on the warehouse's own verdict.
//
// IT QUOTES THE WAREHOUSE AND NEVER THE CALLER. `kind` is the warehouse's own
// vocabulary -- `CREATE_TABLE`, `MERGE`, `EXPORT_DATA`, `SCRIPT` -- and not a
// word anybody typed, which is the rule every refusal on this surface keeps.
// The dialect bounds its length before it gets here.
//
// A SEPARATE SENTENCE FROM THE CLASSIFIER'S, because the two mean different
// things to the person who typed it: one says this service would not read the
// statement, the other says the warehouse read it and it writes.
func notAQuery(kind string) refusal {
	if kind == "" {
		return refusal{http.StatusBadRequest, outcomeWrites,
			"the warehouse did not say what kind of statement this is, and this service " +
				"will not run one it cannot have confirmed is a query"}
	}
	return refusal{http.StatusBadRequest, outcomeWrites, fmt.Sprintf(
		"the warehouse reads this as %s, and this service runs a query. "+
			"Nothing here writes to a warehouse.", kind)}
}

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
	// WHETHER THE WAREHOUSE CAN BE TOLD TO REFUSE, which is a different
	// question from whether the credential may write and the one that
	// decides whether a second service account buys anything. See
	// dialect.ReadOnlyGuard, and `read` below, which is where it is applied.
	// TWO MECHANISMS AND ONE QUESTION. Postgres can be told to refuse writes
	// and then refuses them itself; BigQuery has no read-only session and
	// classifies every statement server-side before running it. Either is
	// the warehouse holding the line, which is what this is asking.
	_, held := conn.(dialect.ReadOnlyGuard)
	_, told := conn.(dialect.Planner)
	guarded := held || told

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
				if guarded {
					outcome = outcomeGuarded
				}
			}
			s.mu.Lock()
			s.probed[table.Connection] = writes
			s.mu.Unlock()
			s.audit(record{Event: "credential", Connection: table.Connection,
				Outcome: outcome}, time.Now())
		}
	}
	// A WAREHOUSE THAT CANNOT BE GUARDED GETS NO RELAXATION. The guarantee
	// one dialect can make is not one another inherits: where nothing but the
	// role holds the line, the role is still what this refuses on.
	if !writes || s.opt.Env == EnvLocal || guarded {
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
func (s *Service) read(ctx context.Context, conn dialect.Conn, table Table, statement string, limit int, line *record) (dialect.Result, error) {
	reader, can := conn.(dialect.Reader)
	if !can {
		return dialect.Result{}, refusal{http.StatusNotImplemented, "unsupported",
			"this warehouse cannot return a result set"}
	}
	// BEFORE ANYTHING IS PRICED OR RUN, and on BOTH endpoints -- which is
	// half of why `read` is one function. A credential that can write makes
	// every other limit on this surface decorative, so it is checked before
	// any of them are applied.
	//
	// NOT ON A CONNECTION SOMEBODY UNLOCKED. The question this answers is
	// whether a credential that may write is the only thing holding the
	// line; where writes are allowed there is no line to hold, and refusing
	// would be refusing the feature.
	if !s.mayWrite(table.Connection) {
		if err := s.assertReadOnly(ctx, conn, table); err != nil {
			return dialect.Result{}, err
		}
	}

	// THE DRY RUN, where there is one. A warehouse that charges for a
	// machine by the hour has nothing to say here and is not refused for
	// staying quiet -- its limits are the row ceiling and the clock.
	//
	// A Planner IS PREFERRED, and it is the same dry run: it carries the
	// price and the warehouse's own verdict on what the statement is, out of
	// one response. Asking an Estimator beside it would be a second round
	// trip for the half this already has.
	var scan int64
	quoted := false
	if told, can := conn.(dialect.Planner); can {
		p, err := told.Plan(ctx, statement)
		if err != nil {
			return dialect.Result{}, refusal{http.StatusBadRequest, "unpriced",
				s.saying("the warehouse would not run this", err)}
		}
		// THE WAREHOUSE OVERRULES THE CLASSIFIER, and this is the only place
		// it can: the tokeniser upstream already passed this statement, and
		// its own comment says what it cannot see inside a function call.
		if !p.Reads {
			if !s.mayWrite(table.Connection) {
				return dialect.Result{}, notAQuery(p.Kind)
			}
			// THE LINE IS STAMPED HERE because this is where it is KNOWN.
			// The handler knows the connection was unlocked, which is a
			// permission; only the warehouse knows the statement used it.
			line.Wrote = true
		}
		scan, quoted = p.Bytes, true
	} else if priced, can := conn.(dialect.Estimator); can {
		var err error
		scan, err = priced.Estimate(ctx, statement)
		quoted = true
		if err != nil {
			// THE WAREHOUSE'S OWN WORDS, and only here, and only to a caller
			// that held a token. A dry run fails on a syntax error or a
			// missing table, which is the one thing the person who typed the
			// SQL needs back; everything else this service says about a
			// warehouse is deliberately vague.
			//
			// WITHOUT A TOKEN IT IS AN EXISTENCE ORACLE -- CHECKPOINT B's
			// F8. `BREVIS_ENV=local` starts with no token, and the banner
			// says what that means: anybody who reaches the port can read
			// the warehouse. On that service, `SELECT * FROM payroll.x`
			// comes back as "Dataset acme-prod:payroll was not found", which
			// answers "does payroll exist" to somebody holding nothing at
			// all. To a caller who DID authenticate it tells them nothing
			// they could not read out of INFORMATION_SCHEMA themselves, and
			// withholding it would cost them the one message they need.
			return dialect.Result{}, refusal{http.StatusBadRequest, "unpriced",
				s.saying("the warehouse would not run this", err)}
		}
	}
	// THE MONEY, ONCE, WHICHEVER ANSWERED. Written below both rather than
	// inside each: a ceiling that lived in one branch would be a ceiling the
	// other warehouse does not have, and nothing would say so.
	if quoted {
		if scan > s.opt.Bytes {
			return dialect.Result{}, tooExpensive(scan, s.opt.Bytes)
		}
		// AND THE SUM, which the per-query ceiling above cannot see.
		// CHECKPOINT B's F6: four queries at a time, each under the ceiling,
		// repeated forever, is unbounded. Checked on the QUOTE because a
		// refusal has to happen before the money is spent, and recorded
		// below on the BILL because the quote is not what gets paid.
		if ok, used := s.spent.allows(table.Connection, scan, s.opt.Budget); !ok {
			return dialect.Result{}, overBudget(scan, used, s.opt.Budget)
		}
	}

	// ONE MORE ROW THAN WILL BE SHOWN. See the preview: a LIMIT inside the
	// statement makes the warehouse's own count agree with what came back,
	// so the extra row is the only thing that can tell a cut from a table
	// that ends there.
	probe := limit + 1
	req := dialect.Request{Query: statement, Limit: probe, MaxBytes: s.opt.Bytes}

	// INSIDE THE WAREHOUSE'S OWN REFUSAL, where there is one.
	//
	// THIS IS THE ONE PLACE CALLER SQL RUNS, which is the other half of why
	// `read` is a single function: the preview builds its statement and the
	// query is typed, and both arrive here. Guarding anywhere else would be
	// guarding a statement this service wrote.
	//
	// ALWAYS AND NOT ONLY FOR A WRITABLE CREDENTIAL. A branch on the probe
	// would be a branch that is wrong in the one case nobody tested -- the
	// probe that could not run, recorded as `unprobed` and treated as a no.
	// Two round trips on a warehouse already being asked a question is not a
	// cost worth a condition.
	var res dialect.Result
	var err error
	if guard, guarded := conn.(dialect.ReadOnlyGuard); guarded && !s.mayWrite(table.Connection) {
		err = guard.ReadOnly(ctx, func(ctx context.Context) error {
			var e error
			res, e = reader.Read(ctx, req)
			return e
		})
	} else {
		res, err = reader.Read(ctx, req)
	}
	if err != nil {
		return dialect.Result{}, err
	}
	// WHAT WAS BILLED, not what was quoted. `Result.Scanned` says why in its
	// own comment: a gap between the two is how somebody finds out that the
	// price a refusal was built on was not the price that was paid.
	s.spent.record(table.Connection, res.Scanned)
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

// saying is a refusal with the warehouse's own message, or without it.
//
// THE LINE IS THE TOKEN AND NOT THE ENVIRONMENT. `BREVIS_ENV` decides
// whether a service MAY run open; what decides whether this caller proved
// anything is whether a token was required at all. A deployment that sets
// one in local gets the full message, and an open service in production --
// which New refuses, but which a future flag might allow -- would not.
func (s *Service) saying(what string, err error) string {
	if s.opt.Token == "" {
		return what + ". Run this service with a token to see what it said."
	}
	return what + ": " + trim(err.Error())
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
// tooExpensive is the ceiling's refusal, and it has ONE definition because
// `/v1/estimate` says the same sentence BEFORE the query is pressed. Two
// copies would be two sentences, and the one the person read in advance
// would stop being the one they get at Run.
func tooExpensive(scan, ceiling int64) refusal {
	return refusal{http.StatusBadRequest, "too-expensive", fmt.Sprintf(
		"this query would scan %s, and this service stops at %s. Narrow the columns, "+
			"or add a filter on a partitioned one.", BytesText(scan), BytesText(ceiling))}
}

type record struct {
	At         string `json:"at"`
	Event      string `json:"event"`
	Connection string `json:"connection,omitempty"`

	// Caller is which named bearer asked, empty when the token carries no
	// name or when the request never authenticated. See caller.go.
	Caller    string `json:"caller,omitempty"`
	Statement string `json:"statement,omitempty"`
	Outcome   string `json:"outcome"`

	// Rows is the ceiling this ran under, Returned how many came back, Bytes
	// what it scanned. All three are zero on a query that never ran, which
	// is the difference worth being able to see.
	Rows     int   `json:"rows,omitempty"`
	Returned int   `json:"returned"`
	Bytes    int64 `json:"bytes"`
	Millis   int64 `json:"ms"`

	// Wrote says the WAREHOUSE read this statement as something that changes
	// data, on a connection where that was allowed.
	//
	// THE VERDICT AND NOT THE PERMISSION. Marking every line from an
	// unlocked connection would be cheaper and would make the field useless
	// on the day somebody greps it, which is the day they are asking what
	// changed.
	Wrote bool `json:"wrote,omitempty"`

	// Estimated is what a DRY RUN quoted, and it is deliberately not Bytes:
	// `observe` adds Bytes to the `scanned` metric per connection, and a dry
	// run scanned nothing. One field for "what we were billed for" and
	// another for "what we were told it would cost" is the difference an
	// operator needs when they read either number.
	Estimated int64 `json:"estimated,omitempty"`
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
	switch line.Event {
	case "query", "preview", "objects", "columns":
		s.met.observe(line.Event, line)
	}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	_, _ = s.opt.Audit.Write(append(b, '\n'))
}
