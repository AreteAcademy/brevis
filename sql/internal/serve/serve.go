package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// errNoConnection is a connector that answered with neither a connection nor
// a reason. See where it is raised.
var errNoConnection = errors.New("the connector returned no connection and no error")

// ErrNoConnection is what an opener returns when nothing is DECLARED for a
// destination, as opposed to declared and unreachable.
//
// THE DIFFERENCE IS THE WHOLE POINT. "Unreachable" is an incident and says
// nothing a reader can act on; "nobody has declared where that database is"
// is a sentence with an action in it, and V4 puts it on the screen. A 502
// would bury the second inside the first.
var ErrNoConnection = errors.New("no connection is declared for that destination")

// EnvLocal is the one environment where an open endpoint is allowed.
const EnvLocal = "local"

// Options is what a Service needs.
type Options struct {
	// Env decides whether an unauthenticated service may exist. It comes from
	// BREVIS_ENV and NEVER from a configuration file -- the gateway's rule,
	// and its reason: "a config file that could declare itself local would be
	// a file that turns off authentication."
	Env string

	// Token is the bearer every request must carry, with NO NAME attached.
	// Required outside local unless Callers names one.
	//
	// It is what every deployment has today and it keeps working. Its audit
	// lines carry no caller, which is itself the finding: a shared secret
	// with nobody's name on it.
	Token string

	// Callers are named bearers, and the audit line carries whichever one
	// asked. See caller.go for why identity is the credential rather than a
	// header. Folded together with Token into one list at boot.
	Callers []Caller

	// Addr is where this will listen, and it is EVIDENCE rather than
	// configuration: see New. Empty means nobody is deploying anything --
	// a test, or something embedding this -- and no rule is drawn from it.
	Addr string

	// Rows is the ceiling. A caller may ask for fewer and cannot ask for more.
	Rows int

	// Bytes is the most a single query may SCAN. A caller cannot raise it
	// and cannot name one at all: it is money, and the browser is not the
	// place that decides how much of it to spend.
	//
	// IT BOUNDS THE PREVIEW TOO, and that was a hole rather than a choice.
	// A preview is `SELECT * FROM t LIMIT 20`, and a LIMIT does not reduce
	// what BigQuery scans -- twenty rows off a petabyte table reads the
	// petabyte and bills for it. The row ceiling bounds the screen and
	// bounds nothing else.
	Bytes int64

	// Budget is the most one CONNECTION may scan in an hour, across every
	// query. Zero is no budget, which is the default.
	//
	// A CONCURRENCY LIMIT IS NOT A BUDGET -- CHECKPOINT B's F6. Four queries
	// at a time, each under the per-query ceiling, repeated forever, is
	// unbounded: the ceiling bounds one query and nothing bounds the sum.
	// For a single authenticated operator no budget may well be the right
	// answer, but it has to be a CHOICE, and an absence is not one. The boot
	// banner states whichever it is.
	//
	// PER CONNECTION, because a connection is what pays the bill: two
	// warehouses are two accounts, and exhausting one must not close the
	// other.
	//
	// IT BOUNDS WHAT IS METERED, which is not everything. A warehouse that
	// charges for a machine by the hour reports no bytes, so a budget bounds
	// BigQuery and bounds nothing on Postgres. That is fine -- Postgres is
	// not billed by the byte -- and dangerous only if somebody believes
	// otherwise, which is why the banner says which connections it can
	// actually bound.
	Budget int64

	// Audit is where one line per query goes. Nil writes none, which is what
	// a test wants and never what a deployment does.
	Audit io.Writer

	// Concurrent is how many queries may run at once. Over that, a caller is
	// REFUSED rather than queued: a queue behind a browser is a browser that
	// waits with no way to know why, and every waiting request still holds a
	// connection to the warehouse.
	Concurrent int

	// Timeout bounds one query, the dry run and the read together.
	Timeout time.Duration

	// Open connects to one warehouse. A field so a test can hand over a fake
	// without a warehouse, and so this package holds no driver of its own.
	//
	// IT TAKES THE WHOLE TARGET and not just a name, because a name is not
	// unique on its own: `app` can be a BigQuery project and a Postgres
	// database at once, and the registry matches on both halves.
	Open func(ctx context.Context, t Table) (dialect.Conn, error)
}

// Service answers read-only questions about a warehouse.
type Service struct {
	opt Options
	// slots is the concurrency limit, held for the length of a query.
	slots chan struct{}

	// probed remembers, per connection, whether its credential can write.
	//
	// ONCE PER CONNECTION AND NOT ONCE PER QUERY: the answer cannot change
	// while an IAM policy does not, and a round trip per query buys nothing.
	// Two first queries at once may both probe -- the probe is free and
	// idempotent, and serialising them behind the lock would put a network
	// call inside it.
	mu     sync.Mutex
	probed map[string]bool

	// listed holds one answer per connection. See objects.
	listed *listings

	// described is one answer per RELATION, which is the level columns are
	// asked at. See columns.go.
	described *descriptions

	// spent is what each connection has scanned in this window. See budget.go.
	spent *spending

	// quoted is how many prices each CALLER has asked for in this window.
	// It exists because a dry run spends nothing, so the budget beside it
	// cannot see one. See estimate.go.
	quoted *quotes

	// callers is every bearer this service accepts, Token folded in with no
	// name. Empty means the service is OPEN.
	callers []Caller

	// met counts what the audit line records. Served on a listener of its
	// own -- see Metrics.
	met *metrics
}

// Defaults for the two limits that are a shape rather than a decision. The
// other two -- rows and bytes -- have none on purpose: a service that picked
// its own row ceiling and its own budget would be a service nobody chose
// either for.
const (
	defaultConcurrent = 4
	defaultTimeout    = 30 * time.Second
)

// New builds the service, or refuses to.
//
// REFUSING TO EXIST IS THE POINT. A service that started without a token and
// logged a warning would be an open endpoint onto a customer's warehouse,
// with a line in a log nobody reads. Failing at boot is the only refusal that
// cannot be ignored.
func New(opt Options) (*Service, error) {
	if opt.Open == nil {
		return nil, errors.New("serve: no way to connect to a warehouse")
	}
	if opt.Rows <= 0 {
		return nil, errors.New("serve: a row ceiling of zero would answer every preview with nothing")
	}
	if opt.Bytes <= 0 {
		return nil, errors.New("serve: a byte ceiling of zero is no ceiling, and the query that " +
			"finds that out is the one nobody meant to run")
	}
	env := strings.TrimSpace(opt.Env)
	if env == "" {
		// AN UNSET BREVIS_ENV ON A ROUTABLE ADDRESS IS REFUSED, and the bind
		// address is why this can be refused at all.
		//
		// The convention across this repository is that an unset BREVIS_ENV
		// means `local`, which means no token; the engine and the gateway
		// both do it, and on a laptop it is right. HERE it means an open
		// endpoint onto a customer's warehouse, reachable by anybody who
		// reaches the port -- and a deployment that forgot one variable gets
		// exactly that, silently.
		//
		// A manifest was the planned fix, and a manifest is a comment
		// somebody can fail to write. A laptop listens on loopback and a pod
		// listens on every interface, so "nobody said, and it is reachable
		// from outside this machine" is the one case that cannot have been
		// meant. Saying `BREVIS_ENV=local` out loud still works anywhere.
		if routable(opt.Addr) {
			return nil, fmt.Errorf("serve: BREVIS_ENV is not set and this would listen on %s, "+
				"which is reachable from outside this machine. An endpoint that reads a "+
				"warehouse without authentication is one anybody who reaches the port can "+
				"read it with. Set BREVIS_ENV and a token, or BREVIS_ENV=%s if you meant "+
				"an open one", opt.Addr, EnvLocal)
		}
		env = EnvLocal
	}
	if env != EnvLocal && opt.Token == "" && len(opt.Callers) == 0 {
		return nil, fmt.Errorf("serve: BREVIS_ENV is %q and no token is set. "+
			"An endpoint that reads a warehouse without authentication is one "+
			"anybody who reaches the port can read it with; only BREVIS_ENV=local "+
			"allows that, and this is not local", env)
	}
	opt.Env = env
	if opt.Concurrent <= 0 {
		opt.Concurrent = defaultConcurrent
	}
	if opt.Timeout <= 0 {
		opt.Timeout = defaultTimeout
	}
	who, err := callers(opt)
	if err != nil {
		return nil, err
	}
	return &Service{
		opt:       opt,
		callers:   who,
		slots:     make(chan struct{}, opt.Concurrent),
		probed:    map[string]bool{},
		listed:    newListings(),
		described: newDescriptions(),
		spent:     newSpending(),
		quoted:    newQuotes(),
		met:       newMetrics(),
	}, nil
}

// routable says whether this address is reachable from another machine.
//
// EMPTY IS NOT ROUTABLE, because an address nobody gave is not a deployment:
// a test and an embedding caller both pass none, and the rule above is about
// what a pod does.
//
// An empty HOST is, though -- `:8088` is every interface, which is what a
// container almost always binds and what a laptop almost never does.
func routable(addr string) bool {
	if addr == "" {
		return false
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Not a shape this understands. Refusing is the safe half of a
		// guess: a service that would not have started is better than one
		// that started open.
		return true
	}
	switch strings.ToLower(host) {
	case "", "0.0.0.0", "::", "[::]":
		return true
	case "localhost":
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip == nil || !ip.IsLoopback()
}

// Handler is the service's routes.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	// POST AND NOT GET. A target in a URL is a target in a proxy log, in a
	// browser's history and in a Referer header; the body keeps it out of all
	// three. `POST /v1/preview` also 405s a GET for free, which is what the
	// method pattern buys over checking r.Method by hand.
	mux.HandleFunc("POST /v1/preview", s.preview)
	// TWO ENDPOINTS AND NOT ONE WITH A FLAG. `/v1/preview` takes a target
	// and composes its own statement, so it cannot be handed SQL at all; a
	// flag on a shared endpoint would make that property a matter of
	// reading the handler correctly.
	mux.HandleFunc("POST /v1/query", s.query)
	// A THIRD ENDPOINT AND NOT A FLAG ON ONE OF THE OTHERS, for the reason
	// preview and query are two: this asks a different question of a
	// different scope, and CHECKPOINT D is the review that says what it may
	// answer.
	mux.HandleFunc("POST /v1/objects", s.objects)

	// WHAT ONE RELATION HOLDS, lazily. CHECKPOINT D put columns at the
	// relation and not at the connection: a project-wide COLUMNS query is
	// the one metadata answer that is genuinely large.
	mux.HandleFunc("POST /v1/columns", s.columns)

	// WHAT A STATEMENT WOULD COST, before it is run. The first endpoint
	// here that spends NOTHING, which is why it is the first one with a
	// rate limit of its own: the budget bounds every other loop on this
	// service by bounding what a loop spends. See CHECKPOINT E and
	// estimate.go.
	mux.HandleFunc("POST /v1/estimate", s.estimate)
	return s.authenticated(mux)
}

// Metrics is the exposition, and it is a SEPARATE HANDLER on purpose.
//
// The rule this repository states twice, in the engine and in the gateway: a
// scrape endpoint on the port that answers requests would either need a
// session no scraper has, or publish every connection name to whoever can
// reach that port. `Handler` does not route /metrics at all, and a test says
// so.
//
// It carries no authentication of its own, for the same reason the gateway's
// does not: it is bound to an address an operator chooses, and a scraper
// cannot hold a token.
func (s *Service) Metrics() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", s.met.handler())
	return mux
}

type previewRequest struct {
	// Target, NOT a statement. An endpoint that cannot be handed SQL cannot
	// be tricked into running any -- which is why preview and query are two
	// endpoints and not one with a flag.
	Target string `json:"target"`
	Limit  int    `json:"limit"`
}

type previewResponse struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
	Limit     int      `json:"limit"`
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) {
	// AUDITED LIKE A QUERY, because it IS one. A preview reads a customer's
	// table, is billed the same bytes and can be refused for the same
	// reasons -- and left no trace at all until the counters were wired and
	// `endpoint="preview"` could never move. #65's criterion is "every query
	// produces exactly one audit line"; this is the half that was missing.
	started := time.Now()
	line := record{Event: "preview", Outcome: "refused"}
	line.Caller = whoAsked(r.Context())
	defer func() { s.audit(line, started) }()

	var req previewRequest
	// A bounded body: a preview request is two fields, and anything larger is
	// not one.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		line.Outcome = "malformed"
		refuse(w, http.StatusBadRequest, "the body is not a preview request")
		return
	}

	// THE TARGET IS PARSED BEFORE ANYTHING IS OPENED. A refusal that happened
	// after connecting would be a refusal that still spent a connection, and
	// a warehouse that saw a request this service had already decided against.
	table, err := ParseTarget(req.Target)
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	line.Connection = table.Connection

	// THE CEILING CLAMPS, IT DOES NOT REFUSE. A preview is not where somebody
	// learns a limit: asking for a million rows is a request that gets the
	// most this will give, not an error. Nothing below zero, nothing above
	// the ceiling, and no limit at all means the ceiling.
	limit := req.Limit
	if limit <= 0 || limit > s.opt.Rows {
		limit = s.opt.Rows
	}
	line.Rows = limit

	ctx := r.Context()
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
			refuseWith(w, http.StatusBadRequest, CodeNoConnection, ErrNoConnection.Error())
			return
		}
		// Everything else is not forwarded: it is a connection error from a
		// driver and may carry a host, a role or a project somebody is not
		// meant to learn from a 502.
		refuse(w, http.StatusBadGateway, "the warehouse could not be reached")
		return
	}
	defer func() { _ = conn.Close(context.Background()) }()

	// Composed HERE, from a target that has already been proven to be two
	// names BigQuery could hold. Nothing a caller sent reaches this string.
	//
	// IT ASKS FOR ONE MORE ROW THAN IT WILL SHOW, and that is not a detail.
	// The LIMIT is in the statement, so the query MATCHES exactly what it
	// returns and the warehouse's own `totalRows` can never report a cut --
	// a five-row table under a ceiling of three came back with three rows
	// and `truncated: false`, found by running it. The warehouse was telling
	// the truth; the question was wrong.
	//
	// One extra row costs nothing and is the whole mechanism: more than the
	// ceiling came back means there is more, and the extra is dropped rather
	// than drawn.
	stmt := "SELECT * FROM " + table.Relation + " LIMIT " + strconv.Itoa(limit+1)

	res, err := s.read(ctx, conn, table, stmt, limit)
	if err != nil {
		var no refusal
		if errors.As(err, &no) {
			line.Outcome = no.outcome
			refuse(w, no.code, no.why)
			return
		}
		line.Outcome = "failed"
		refuse(w, http.StatusBadGateway, "the warehouse refused the preview")
		return
	}
	line.Outcome = "ok"
	line.Bytes = res.Scanned
	line.Returned = len(res.Rows)
	write(w, http.StatusOK, previewResponse{
		Columns: res.Columns, Rows: res.Rows, Truncated: res.Truncated, Limit: limit,
	})
}

// BytesText is a byte count somebody can judge at a glance.
//
// A refusal saying "10737418240 bytes" is one somebody has to count the
// digits of, and the number IS the content of that message: it is the
// difference between a typo and a query worth waiting for.
func BytesText(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.3g TB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.3g GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.3g MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.3g kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func refuse(w http.ResponseWriter, code int, why string) {
	write(w, code, map[string]string{"error": why})
}

// refuseWith is a refusal the CALLER has to act on differently, and so
// carries a machine-readable code beside the sentence.
//
// A CODE AND NOT A STRING MATCH. The console turns "nothing is declared for
// that destination" into a sentence naming the database and the file to put
// it in -- it can, because it holds the target, and this service must not:
// its refusals never echo their input. Matching on the sentence would make
// this service's WORDING part of its contract, and the next person to
// improve a message would break a screen.
func refuseWith(w http.ResponseWriter, status int, code, why string) {
	write(w, status, map[string]string{"error": why, "code": code})
}

// CodeNoConnection is what the console matches on. See ErrNoConnection.
const CodeNoConnection = "no-connection"

func write(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
