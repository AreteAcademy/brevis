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

type objectsRequest struct {
	// Target names the CONNECTION. Its relation half is not used: this asks
	// what the whole warehouse holds, which is the only question worth the
	// round trip.
	Target string `json:"target"`
}

// relation is one name on the WIRE.
//
// A SEPARATE TYPE FROM `dialect.Relation`, which is the rule `/v1/query`
// already follows with `dialect.Result`: a domain type has no tags, so
// marshalling one directly published `{"Schema":…,"Name":…}` from this
// endpoint while every other response here is lowercase. The console's
// decoder is case-insensitive and hid it; curling the running service did
// not.
type relation struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

func wire(rels []dialect.Relation) []relation {
	out := make([]relation, 0, len(rels))
	for _, r := range rels {
		out = append(out, relation{Schema: r.Schema, Name: r.Name})
	}
	return out
}

type objectsResponse struct {
	Relations []relation `json:"relations"`

	// Truncated says the ceiling cut the answer, which a tree has to draw:
	// a browser silently missing half a warehouse is worse than one that
	// says it is showing part.
	Truncated bool `json:"truncated"`
}

// relationCeiling bounds one listing.
//
// NOT A COST LIMIT -- the cost is two queries at a fixed floor, whatever
// comes back -- but a SIZE one: a warehouse with a hundred thousand
// relations would otherwise be a hundred thousand rows crossing the wire and
// a hundred thousand nodes in a browser, for a sidebar nobody can read.
const relationCeiling = 5000

// listing is one connection's answer, and when it was taken.
type listing struct {
	rels []dialect.Relation
	cut  bool
	at   time.Time
}

// listingTTL is how long an answer is held.
//
// HELD AT ALL BECAUSE IT COSTS MONEY: measured against BigQuery, every
// INFORMATION_SCHEMA query is billed at a 10 MiB floor whatever comes back,
// so a tree that listed on every page view would spend 20 MB per visit. Five
// minutes is long enough that clicking around is free and short enough that
// a table created during a session shows up in one.
const listingTTL = 5 * time.Minute

type listings struct {
	mu  sync.Mutex
	has map[string]listing
}

func newListings() *listings { return &listings{has: map[string]listing{}} }

// objects says what a connection holds.
//
// A LISTING IS A DIFFERENT DISCLOSURE FROM A QUERY, which is why it took its
// own checkpoint. `serve`'s other two endpoints reach only what the CATALOG
// knows, because a target comes from something that landed; this reaches
// what the credential can see.
//
// What makes that acceptable is that a query already could: somebody with
// this endpoint's access can `SELECT` from anything the credential reads, so
// a listing reveals NAMES of things already readable. The increment is
// discovery, not access -- and the read-only role, asserted at first use, is
// what makes that sentence safe to say.
//
// IT IS NOT PRICED, and that is a deliberate exception to the rule every
// other query here follows. A dry run of a 10 MiB metadata query is itself a
// 10 MiB metadata query: pricing this would double its cost to learn a
// number that is fixed by construction. The bound here is the ceiling on
// ROWS, above, and the cache.
func (s *Service) objects(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	line := record{Event: "objects", Outcome: "refused"}
	line.Caller = whoAsked(r.Context())
	defer func() { s.audit(line, started) }()

	var req objectsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		line.Outcome = "malformed"
		refuse(w, http.StatusBadRequest, "the body is not a request for a connection's objects")
		return
	}
	table, err := ParseTarget(req.Target)
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	line.Connection = table.Connection

	if held, ok := s.listed.get(table.Connection); ok {
		line.Outcome = "cached"
		line.Returned = len(held.rels)
		write(w, http.StatusOK, objectsResponse{Relations: wire(held.rels), Truncated: held.cut})
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

	l, can := conn.(dialect.Lister)
	if !can {
		line.Outcome = "unsupported"
		refuse(w, http.StatusNotImplemented, "this warehouse cannot say what it holds")
		return
	}
	// THE CREDENTIAL IS CHECKED HERE TOO. A listing is a round trip to a
	// warehouse on a credential that must not be able to write, and the
	// probe is once per connection whichever endpoint asks first.
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

	rels, err := l.Relations(ctx)
	if err != nil {
		line.Outcome = "failed"
		refuse(w, http.StatusBadGateway, "the warehouse would not say what it holds")
		return
	}
	cut := len(rels) > relationCeiling
	if cut {
		rels = rels[:relationCeiling]
	}
	if rels == nil {
		rels = []dialect.Relation{}
	}
	s.listed.put(table.Connection, listing{rels: rels, cut: cut, at: time.Now()})

	line.Outcome = "ok"
	line.Returned = len(rels)
	write(w, http.StatusOK, objectsResponse{Relations: wire(rels), Truncated: cut})
}

func (c *listings) get(connection string) (listing, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, ok := c.has[connection]
	if !ok || time.Since(held.at) > listingTTL {
		return listing{}, false
	}
	return held, true
}

func (c *listings) put(connection string, held listing) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.has[connection] = held
}
