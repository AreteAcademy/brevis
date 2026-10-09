// Package sqlserve asks `brevis-sql serve` for a preview or for a query.
//
// THE ENGINE'S FIRST OUTBOUND CALL, and the reason it is a package rather
// than six lines in a handler: a thing that reaches out has a timeout, a set
// of failure modes a screen has to show, and a rule about which of the other
// service's words may be repeated. Those are decisions, and they belong
// somewhere they can be read and tested.
//
// IT HOLDS NO WAREHOUSE CREDENTIAL AND NO DRIVER. That is the whole point of
// `serve` existing: the console forwards a question and never learns how to
// answer it. `engine-weight.sh` is the test of that claim.
package sqlserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// serveTimeout is the SERVICE's own bound on one query, mirrored here
// because the engine does not import the sql module and never will.
//
// A NUMBER THIS CANNOT IMPORT, SO IT SAYS WHERE IT CAME FROM:
// `sql/internal/serve.defaultTimeout`. If that moves, this moves.
const serveTimeout = 30 * time.Second

// timeout bounds one call, and it is LONGER than the service's.
//
// It was ten seconds, and the service runs for thirty. CHECKPOINT B's F5: a
// query taking eleven told the reader "the SQL service could not be reached"
// while the warehouse ran it to completion and billed for it. The reader was
// told the opposite of what happened and paid for it.
//
// Whoever refuses has to be the one who knows why. The service bounds the
// query, says so in a sentence and writes an audit line; this waits long
// enough for that sentence to arrive. The slack is for the round trip, not
// for more query.
const timeout = serveTimeout + 5*time.Second

// Result is what came back, as a page needs it.
type Result struct {
	Columns []string `json:"columns"`
	// Rows holds `any` because NULL IS NOT THE EMPTY STRING: one is "nothing
	// was recorded" and the other is a value somebody wrote, and they are
	// drawn differently. A nil cell is NULL, all the way to the template.
	Rows      [][]any `json:"rows"`
	Truncated bool    `json:"truncated"`
	Limit     int     `json:"limit"`

	// Bytes is what the warehouse says it scanned, Millis how long it took.
	// Both are zero for a preview, which does not report them: a preview is
	// a glance and nobody chose its cost. For a query they go UNDER THE GRID,
	// because the difference between the query somebody wrote and the one
	// that was cheap is the thing a query tab can actually teach.
	Bytes  int64 `json:"bytes"`
	Millis int64 `json:"ms"`
}

// Relation is one thing a SELECT could name, as the tree draws it.
type Relation struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// Objects is what a connection holds.
type Objects struct {
	Relations []Relation `json:"relations"`
	// Truncated says the service cut the answer, which the tree has to draw:
	// a browser silently missing half a warehouse is worse than one that
	// says it is showing part.
	Truncated bool `json:"truncated"`
}

// Client reaches one `brevis-sql serve`.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New builds a client. An empty base is a console with no service configured,
// and Preview says so rather than dialling nothing.
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimSuffix(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: timeout},
	}
}

// Configured says whether there is a service to ask. The page uses it to
// decide whether a Preview tab exists at all -- a tab that always answers
// "not configured" is a tab nobody wants.
func (c *Client) Configured() bool { return c != nil && c.base != "" }

// ErrNotConfigured is "nobody told this console where serve is".
var ErrNotConfigured = errors.New("no SQL service is configured for this console")

// ErrNoConnection is `serve` saying nothing is DECLARED for that destination,
// as opposed to declared and unreachable.
//
// MATCHED ON A CODE AND NEVER ON THE SENTENCE. The service's wording is not
// its contract -- the next person to improve a message would otherwise break
// a screen -- and the sentence it sends names nothing, deliberately, because
// its refusals never echo their input. The page says which database, because
// the page holds the target.
var ErrNoConnection = errors.New("no connection is declared for that destination")

// codeNoConnection is `serve`'s own constant, copied rather than imported:
// the engine does not depend on the sql module and never will, which is what
// engine-weight.sh asserts. A wire protocol is the one thing two modules may
// agree on without sharing code, and this is the whole of the agreement.
const codeNoConnection = "no-connection"

type request struct {
	Target string `json:"target"`

	// Schema and Name name a RELATION, for the one endpoint that asks about
	// one: a tree's nodes come from a listing and a listing answers in these
	// two fields, so a relation nothing landed on still has a name here.
	Schema string `json:"schema,omitempty"`
	Name   string `json:"name,omitempty"`

	// Statement is empty for a preview, which composes its own: the preview
	// endpoint cannot be handed SQL at all, and this is the console's half
	// of that -- there is nothing to leave out by mistake.
	Statement string `json:"statement,omitempty"`
	Limit     int    `json:"limit"`
}

// Preview asks for the first rows of a destination.
//
// WHAT IT REPEATS AND WHAT IT SWALLOWS is the decision here. A 400 from
// `serve` is a sentence written to be read by a person -- "that target is not
// a table" -- and it already refuses to echo its input, so it is carried
// through. Everything else is not: a 401 means this console's token is wrong,
// which is an operator's problem and not a reader's, and a 502 carries a
// host, a role or a project from a driver. Repeating either tells a browser
// about a service it cannot reach and should not learn about.
func (c *Client) Preview(ctx context.Context, target string, limit int) (Result, error) {
	return c.ask(ctx, "/v1/preview", request{Target: target, Limit: limit}, "preview")
}

// Query runs a statement somebody typed.
//
// THE CONSOLE DOES NOT READ THE SQL. It does not classify it, does not count
// its statements and does not price it: every one of those is a limit, a
// limit the caller can change is not a limit, and the caller here is a
// browser. `serve` decides, and what comes back is either rows or a sentence.
func (c *Client) Query(ctx context.Context, target, statement string, limit int) (Result, error) {
	return c.ask(ctx, "/v1/query",
		request{Target: target, Statement: statement, Limit: limit}, "query")
}

// Objects asks what a connection holds, for the workbench's tree.
//
// IT COSTS MONEY THE FIRST TIME and nothing afterwards: the service holds
// one answer per connection, because every metadata query on BigQuery is
// billed at a 10 MiB floor whatever comes back. This console asks on each
// render and the service decides whether that is a round trip.
func (c *Client) Objects(ctx context.Context, target string) (Objects, error) {
	var out Objects
	if !c.Configured() {
		return out, ErrNotConfigured
	}
	raw, err := json.Marshal(request{Target: target})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/objects", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return out, errors.New("the SQL service could not be reached")
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return out, refusal(res, "listing")
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 8<<20)).Decode(&out); err != nil {
		return out, errors.New("the SQL service answered something this console cannot read")
	}
	return out, nil
}

// Column is one field of a relation.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Columns asks what ONE relation holds, for the tree's open node.
//
// LAZY, AND THAT IS THE POINT. `Objects` answers for a whole connection in
// one call because BigQuery bills a flat floor per metadata query; columns
// invert it -- a project-wide COLUMNS query is the one metadata answer that
// is genuinely large. So this console asks only about what somebody opened,
// and the service holds the answer per relation.
func (c *Client) Columns(ctx context.Context, target, schema, name string) ([]Column, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	raw, err := json.Marshal(request{Target: target, Schema: schema, Name: name})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/columns", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("the SQL service could not be reached")
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, refusal(res, "listing")
	}
	var out struct {
		Columns []Column `json:"columns"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 8<<20)).Decode(&out); err != nil {
		return nil, errors.New("the SQL service answered something this console cannot read")
	}
	return out.Columns, nil
}

// ask is one call.
//
// WHAT IT REPEATS AND WHAT IT SWALLOWS is the decision here. A 400 from
// `serve` is a sentence written to be read by a person -- "that target is not
// a table", "this would scan 4 TB" -- and it already refuses to echo its
// input, so it is carried through. Everything else is not: a 401 means this
// console's token is wrong, which is an operator's problem and not a
// reader's, and a 502 carries a host, a role or a project from a driver.
// Repeating either tells a browser about a service it cannot reach and should
// not learn about.
func (c *Client) ask(ctx context.Context, path string, body request, what string) (Result, error) {
	if !c.Configured() {
		return Result{}, ErrNotConfigured
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		// THE ADDRESS DOES NOT REACH THE MESSAGE. A transport error carries
		// the host and port, and this one renders on a page: an internal
		// address is not a reader's business and a screenshot travels.
		return Result{}, errors.New("the SQL service could not be reached")
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return Result{}, refusal(res, what)
	}

	var out Result
	// Bounded: a hundred rows, and a service answering something else is not
	// one this console should try to hold.
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 8<<20)).Decode(&out); err != nil {
		return Result{}, errors.New("the SQL service answered something this console cannot read")
	}
	return out, nil
}

// refusal turns a non-200 into what a page may say.
func refusal(res *http.Response, what string) error {
	if res.StatusCode == http.StatusBadRequest {
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 8<<10)).Decode(&body); err == nil {
			if body.Code == codeNoConnection {
				return ErrNoConnection
			}
			if strings.TrimSpace(body.Error) != "" {
				return errors.New(body.Error)
			}
		}
	}
	// NOT THE SERVICE'S WORDS, and not its status text either -- just the
	// number, which is enough for an operator reading a screenshot and says
	// nothing to anybody else.
	return fmt.Errorf("the SQL service refused this %s (%d)", what, res.StatusCode)
}
