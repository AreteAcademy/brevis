// Package sqlserve asks `brevis-sql serve` for a preview.
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

// timeout bounds one preview.
//
// A console request blocked on a warehouse is a browser tab that hangs, and
// a preview is a glance rather than a report: ten seconds is longer than any
// preview worth drawing and shorter than anybody's patience.
const timeout = 10 * time.Second

// Result is a preview, as a page needs it.
type Result struct {
	Columns []string `json:"columns"`
	// Rows holds `any` because NULL IS NOT THE EMPTY STRING: one is "nothing
	// was recorded" and the other is a value somebody wrote, and they are
	// drawn differently. A nil cell is NULL, all the way to the template.
	Rows      [][]any `json:"rows"`
	Truncated bool    `json:"truncated"`
	Limit     int     `json:"limit"`
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

type request struct {
	Target string `json:"target"`
	Limit  int    `json:"limit"`
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
	if !c.Configured() {
		return Result{}, ErrNotConfigured
	}

	body, err := json.Marshal(request{Target: target, Limit: limit})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/preview", bytes.NewReader(body))
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
		return Result{}, refusal(res)
	}

	var out Result
	// Bounded: a preview is a hundred rows, and a service answering something
	// else is not one this console should try to hold.
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 8<<20)).Decode(&out); err != nil {
		return Result{}, errors.New("the SQL service answered something this console cannot read")
	}
	return out, nil
}

// refusal turns a non-200 into what a page may say.
func refusal(res *http.Response) error {
	if res.StatusCode == http.StatusBadRequest {
		var body struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 8<<10)).Decode(&body); err == nil &&
			strings.TrimSpace(body.Error) != "" {
			return errors.New(body.Error)
		}
	}
	// NOT THE SERVICE'S WORDS, and not its status text either -- just the
	// number, which is enough for an operator reading a screenshot and says
	// nothing to anybody else.
	return fmt.Errorf("the SQL service refused this preview (%d)", res.StatusCode)
}
