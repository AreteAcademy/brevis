package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

type columnsRequest struct {
	// Target names the CONNECTION, as it does for a listing.
	Target string `json:"target"`

	// Schema and Name are the relation, in the two fields a listing answers
	// in -- and not the target's tail, because a tree's nodes come from a
	// listing and a relation nothing landed on has no target of its own.
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// column is one field on the WIRE. Its own type with explicit tags, which is
// the rule every response here follows -- see objects.go for the day a
// domain type was marshalled directly and shipped capitalised keys.
type column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type columnsResponse struct {
	Columns []column `json:"columns"`

	// Truncated says the ceiling cut the answer, which a tree has to draw.
	Truncated bool `json:"truncated"`
}

// columnCeiling bounds one relation's answer.
//
// A size bound and not a cost one, like the listing's: BigQuery allows 10,000
// columns in a table, and a sidebar that tried to draw them would be a
// browser hanging on a click nobody meant to be expensive.
const columnCeiling = 500

// described is one relation's answer, and when it was taken.
type described struct {
	cols []dialect.Column
	cut  bool
	at   time.Time
}

type descriptions struct {
	mu  sync.Mutex
	has map[string]described
}

func newDescriptions() *descriptions { return &descriptions{has: map[string]described{}} }

// columns says what one relation holds.
//
// LAZY, WHICH IS THE WHOLE SHAPE. The listing beside it answers for a whole
// connection in one call because BigQuery bills a flat 10 MiB floor for any
// metadata query, so asking per node would be the expensive mistake. Columns
// invert that: a project-wide COLUMNS query is the one metadata answer that
// is genuinely large, and nobody expands a relation by accident.
//
// IT DISCLOSES NO MORE THAN A PREVIEW ALREADY DOES. `/v1/preview` answers
// `{"columns":[...]}` for any destination in the catalog; this answers the
// same kind of fact for a relation the credential can read. The increment is
// discovery, not access -- CHECKPOINT D, §4.4 -- and the read-only role,
// asserted at first use, is what makes that sentence safe to say.
func (s *Service) columns(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	line := record{Event: "columns", Outcome: "refused"}
	line.Caller = whoAsked(r.Context())
	defer func() { s.audit(line, started) }()

	var req columnsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		line.Outcome = "malformed"
		refuse(w, http.StatusBadRequest, "the body is not a request for a relation's columns")
		return
	}
	table, err := ParseTarget(req.Target)
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	line.Connection = table.Connection

	// BOTH HALVES, OR NEITHER. A relation with no schema is not a relation
	// this can ask about: every warehouse here names one in two parts, and
	// guessing the missing half would be guessing which table somebody meant.
	//
	// The names themselves are NOT in the audit line -- CHECKPOINT D's rule
	// that a browse carries the connection and the level and never a name.
	if req.Schema == "" || req.Name == "" {
		line.Outcome = "incomplete"
		refuse(w, http.StatusBadRequest, "a relation is named by a schema and a name, and one of them is missing")
		return
	}
	rel := dialect.Relation{Schema: req.Schema, Name: req.Name}

	// PER RELATION, which is what the key says. A cache on the connection
	// alone would answer every table with the first one's columns.
	key := table.Connection + "\x00" + rel.Schema + "\x00" + rel.Name
	if held, ok := s.described.get(key); ok {
		line.Outcome = "cached"
		line.Returned = len(held.cols)
		write(w, http.StatusOK, columnsResponse{Columns: wireColumns(held.cols), Truncated: held.cut})
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

	d, can := conn.(dialect.Describer)
	if !can {
		line.Outcome = "unsupported"
		refuse(w, http.StatusNotImplemented, "this warehouse cannot say what a relation holds")
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

	cols, err := d.Columns(ctx, rel)
	if err != nil {
		line.Outcome = "failed"
		refuse(w, http.StatusBadGateway, "the warehouse would not say what that relation holds")
		return
	}
	cut := len(cols) > columnCeiling
	if cut {
		cols = cols[:columnCeiling]
	}
	s.described.put(key, described{cols: cols, cut: cut, at: time.Now()})

	line.Outcome = "ok"
	line.Returned = len(cols)
	write(w, http.StatusOK, columnsResponse{Columns: wireColumns(cols), Truncated: cut})
}

func wireColumns(cols []dialect.Column) []column {
	out := make([]column, 0, len(cols))
	for _, c := range cols {
		out = append(out, column{Name: c.Name, Type: c.Type})
	}
	return out
}

func (c *descriptions) get(key string) (described, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, ok := c.has[key]
	if !ok || time.Since(held.at) > listingTTL {
		return described{}, false
	}
	return held, true
}

func (c *descriptions) put(key string, held described) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.has[key] = held
}
