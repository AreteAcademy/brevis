package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// estimateRate is how many statements one caller may have priced in a
// window, and estimateWindow is the window.
//
// WHY THIS ENDPOINT NEEDS A LIMIT AT ALL, when nothing else here has one: a
// dry run scans nothing, so it spends no budget -- and the budget is the
// thing that stops a loop on every other endpoint. Free to call is free to
// call forever. On BigQuery a dry run costs no money and still counts
// against the project's API quota, which is the client's and not this
// service's to burn.
//
// 120 A MINUTE, AND THE NUMBER IS AN ARGUMENT RATHER THAN A ROUND ONE. The
// console asks once per pause in typing and never twice for the same text,
// so one operator produces something like ten or twenty a minute at the
// outside; a shared token is one caller here, so the number has to hold
// several of them. A loop produces thousands. Anything in that gap stops the
// loop without ever being felt by a person, and this sits near the top of it.
//
// A FIXED WINDOW, like the budget's, and with the same stated cost: the real
// worst case over any minute is twice the number, at a boundary. For a
// ceiling whose job is to stop a runaway rather than to ration anybody, that
// is the right trade.
const (
	estimateRate   = 120
	estimateWindow = time.Minute
)

// quotes is how many prices each caller has asked for in this window.
type quotes struct {
	mu   sync.Mutex
	n    map[string]int
	from time.Time
}

func newQuotes() *quotes { return &quotes{n: map[string]int{}, from: time.Now()} }

// allows counts this call and says whether it may happen.
//
// THE KEY IS THE CALLER AND NOT THE CONNECTION. The thing being rationed is
// somebody's ability to call in a loop, and a loop is written by a caller.
// An unnamed shared token is one key, which is the honest reading of a
// shared secret: it is one identity, and that is the finding caller.go
// already records about it.
func (q *quotes) allows(who string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if time.Since(q.from) >= estimateWindow {
		q.n = map[string]int{}
		q.from = time.Now()
	}
	if q.n[who] >= estimateRate {
		return false
	}
	q.n[who]++
	return true
}

// rewind moves the window's start back so a test can reach the next one
// without waiting. It changes nothing a request can see.
func (q *quotes) rewind(d time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.from = q.from.Add(-d)
}

type estimateRequest struct {
	Target    string `json:"target"`
	Statement string `json:"statement"`
}

type estimateResponse struct {
	// Bytes is what the warehouse says the statement would process. It is
	// the whole answer: no rows, no columns, no plan.
	Bytes int64 `json:"bytes"`
}

// estimate prices a statement without running it.
//
// WHAT IT DISCLOSES, which is the question CHECKPOINT E asked first: the
// bytes a statement would scan, to a caller who already holds a token and
// could learn the same number by running it. It is strictly LESS than
// `/v1/query` answers for the same statement -- that one returns the price
// and the rows.
//
// WHAT IT IS FOR: a refusal that arrives before the money is spent. A
// statement over the ceiling is refused here with the sentence it would get
// at Run, which is the only moment at which refusing is worth anything.
func (s *Service) estimate(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	line := record{Event: "estimate", Outcome: "refused"}
	line.Caller = whoAsked(r.Context())
	defer func() { s.audit(line, started) }()

	var req estimateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		line.Outcome = "malformed"
		refuse(w, http.StatusBadRequest, "the body is not a request to price a statement")
		return
	}
	table, err := ParseTarget(req.Target)
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	line.Connection = table.Connection

	// THE HASH, AND NEVER THE TEXT -- the rule `/v1/query` already follows.
	// It is what ties a price to the query that followed it, which is the
	// one question an auditor asks of this endpoint.
	sum := sha256.Sum256([]byte(req.Statement))
	line.Statement = hex.EncodeToString(sum[:])[:16]

	// THE RATE FIRST, BEFORE ANYTHING IS SPENT. A refusal that happened
	// after a connection is a refusal that opened one, which is exactly what
	// a loop would be doing.
	if !s.quoted.allows(line.Caller) {
		line.Outcome = "too-many"
		refuse(w, http.StatusTooManyRequests, fmt.Sprintf(
			"this service prices at most %d statements a minute and this caller has asked for that many. "+
				"It will answer again when the minute turns.", estimateRate))
		return
	}

	// THE CLASSIFIER, as on /v1/query. This endpoint takes SQL, so it can be
	// handed a DELETE -- and a dry run of a DELETE is still a statement this
	// service has decided it will not put in front of a warehouse.
	if err := ReadOnly(table.Dialect, req.Statement); err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}

	// A SLOT, AND NEVER A WAIT FOR ONE. A price is worth less than an
	// answer: when this service is already running as many queries as it
	// will, the honest thing is to say so at once rather than to hold a
	// caller -- and to leave the slot for the query somebody pressed.
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		line.Outcome = "busy"
		refuse(w, http.StatusServiceUnavailable,
			"this service is already running as many queries as it will run at once")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.opt.Timeout)
	defer cancel()

	conn, err := s.opt.Open(ctx, table)
	if err == nil && conn == nil {
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

	// NO FALLBACK, AND THAT IS A DECISION SOMEBODY MADE. Postgres has no
	// Estimator, and `EXPLAIN` is the obvious substitute: it was refused at
	// CHECKPOINT E. It returns a cost in planner units nobody is billed in,
	// and a number in the wrong unit under a line that says what a query
	// will process is worse than no number -- somebody would compare it with
	// a BigQuery figure and act on the comparison.
	pricer, can := conn.(dialect.Estimator)
	if !can {
		line.Outcome = "unsupported"
		refuse(w, http.StatusNotImplemented, "this warehouse does not price a query before it runs")
		return
	}
	// THE CREDENTIAL IS CHECKED HERE TOO, once per connection whichever
	// endpoint asks first.
	if err := s.assertReadOnly(ctx, conn, table); err != nil {
		var no refusal
		if errors.As(err, &no) {
			line.Outcome = no.outcome
			refuse(w, no.code, no.why)
			return
		}
		line.Outcome = "failed"
		refuse(w, http.StatusBadGateway, "the warehouse could not be asked")
		return
	}

	scan, err := pricer.Estimate(ctx, req.Statement)
	if err != nil {
		// THE WAREHOUSE'S OWN WORDS, under the same rule `read` states: a
		// dry run fails on a syntax error or a missing table, which is the
		// one thing the person who typed the SQL needs back.
		line.Outcome = "unpriced"
		refuse(w, http.StatusBadRequest, s.saying("the warehouse would not run this", err))
		return
	}

	// `Estimated` AND NOT `Bytes`. That field is added to the `scanned`
	// metric per connection, and a dry run scanned nothing: counting it
	// there would put bytes nobody was billed for into the one number an
	// operator reads to answer "what did we spend".
	line.Estimated = scan

	if scan > s.opt.Bytes {
		no := tooExpensive(scan, s.opt.Bytes)
		line.Outcome = no.outcome
		refuse(w, no.code, no.why)
		return
	}

	// AND THE BUDGET IS NOT CONSULTED, deliberately. It is a MOVING
	// condition: what the hour has left changes between this answer and the
	// Run that follows it, so a line saying "this would be over budget"
	// would be a promise about a number that moves. The ceiling above does
	// not move, which is why that one is worth saying early.
	line.Outcome = "ok"
	write(w, http.StatusOK, estimateResponse{Bytes: scan})
}
