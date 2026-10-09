package serve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// EnvLocal is the one environment where an open endpoint is allowed.
const EnvLocal = "local"

// Options is what a Service needs.
type Options struct {
	// Env decides whether an unauthenticated service may exist. It comes from
	// BREVIS_ENV and NEVER from a configuration file -- the gateway's rule,
	// and its reason: "a config file that could declare itself local would be
	// a file that turns off authentication."
	Env string

	// Token is the bearer every request must carry. Required outside local.
	Token string

	// Rows is the ceiling. A caller may ask for fewer and cannot ask for more.
	Rows int

	// Open connects to one warehouse. A field so a test can hand over a fake
	// without a warehouse, and so this package holds no driver of its own.
	Open func(ctx context.Context, connection string) (dialect.Conn, error)
}

// Service answers read-only questions about a warehouse.
type Service struct {
	opt Options
}

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
	env := strings.TrimSpace(opt.Env)
	if env == "" {
		env = EnvLocal
	}
	if env != EnvLocal && opt.Token == "" {
		return nil, fmt.Errorf("serve: BREVIS_ENV is %q and no token is set. "+
			"An endpoint that reads a warehouse without authentication is one "+
			"anybody who reaches the port can read it with; only BREVIS_ENV=local "+
			"allows that, and this is not local", env)
	}
	opt.Env = env
	return &Service{opt: opt}, nil
}

// Handler is the service's routes.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	// POST AND NOT GET. A target in a URL is a target in a proxy log, in a
	// browser's history and in a Referer header; the body keeps it out of all
	// three. `POST /v1/preview` also 405s a GET for free, which is what the
	// method pattern buys over checking r.Method by hand.
	mux.HandleFunc("POST /v1/preview", s.preview)
	return s.authenticated(mux)
}

// authenticated refuses anything without the bearer, where one is required.
func (s *Service) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opt.Token != "" {
			given, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			// CONSTANT TIME, because the comparison's duration is otherwise a
			// measurement of how much of the token is right.
			if subtle.ConstantTimeCompare([]byte(given), []byte(s.opt.Token)) != 1 {
				refuse(w, http.StatusUnauthorized, "a valid bearer token is required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
	var req previewRequest
	// A bounded body: a preview request is two fields, and anything larger is
	// not one.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
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

	// THE CEILING CLAMPS, IT DOES NOT REFUSE. A preview is not where somebody
	// learns a limit: asking for a million rows is a request that gets the
	// most this will give, not an error. Nothing below zero, nothing above
	// the ceiling, and no limit at all means the ceiling.
	limit := req.Limit
	if limit <= 0 || limit > s.opt.Rows {
		limit = s.opt.Rows
	}

	ctx := r.Context()
	conn, err := s.opt.Open(ctx, table.Connection)
	if err != nil {
		// The reason is not forwarded: it is a connection error from a driver
		// and may carry a host, a role or a project somebody is not meant to
		// learn from a 502.
		refuse(w, http.StatusBadGateway, "the warehouse could not be reached")
		return
	}
	defer func() { _ = conn.Close(context.Background()) }()

	reader, can := conn.(dialect.Reader)
	if !can {
		refuse(w, http.StatusNotImplemented, "this warehouse cannot return a result set")
		return
	}

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
	probe := limit + 1
	stmt := "SELECT * FROM " + table.Relation + " LIMIT " + strconv.Itoa(probe)

	res, err := reader.Read(ctx, stmt, probe)
	if err != nil {
		refuse(w, http.StatusBadGateway, "the warehouse refused the preview")
		return
	}
	truncated := len(res.Rows) > limit
	if truncated {
		res.Rows = res.Rows[:limit]
	}

	// An empty result is `[]` and never `null`: a grid iterating over null is
	// a grid that throws, and "no rows" is an answer.
	if res.Rows == nil {
		res.Rows = [][]any{}
	}
	if res.Columns == nil {
		res.Columns = []string{}
	}
	write(w, http.StatusOK, previewResponse{
		Columns: res.Columns, Rows: res.Rows, Truncated: truncated, Limit: limit,
	})
}

func refuse(w http.ResponseWriter, code int, why string) {
	write(w, code, map[string]string{"error": why})
}

func write(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
