// Package endpointcall runs a published endpoint for the console's "Try it",
// against the brevis-sql-api that serves it.
//
// THE ENGINE'S SECOND OUTBOUND CALL, and sqlserve's shape: the console
// forwards a question to the service that holds the warehouse credential and
// never learns how to answer it. It holds no driver and no DSN -- an API's
// address and one key, both from the environment.
//
// ONE KEY FOR THE CONSOLE, and that is a decision with a consequence that is
// said where it is configured: every console login runs endpoints as THAT
// key's tenant. Which is why the panel only exists on a console that asks
// who you are, as the SQL tabs do.
package endpointcall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// timeout is past the API's own default (10s) and its ceiling on a slow
// warehouse, so the API is the one that says why it stopped.
const timeout = 30 * time.Second

// maxBody bounds what the console reads back. The API caps an answer at 8
// MiB by default and 64 at most; a page is not where either belongs whole.
const maxBody = 4 << 20

// ErrNotConfigured is an API the engine was given no address for.
var ErrNotConfigured = errors.New("no address is configured for that API")

// Client runs endpoints on the APIs it has addresses for.
type Client struct {
	urls map[string]*url.URL
	key  string
	http *http.Client
}

// New reads `api=url` pairs, separated by commas or newlines, and the key to
// send. An empty list is a client that has nothing configured.
//
// AN ERROR NEVER QUOTES A URL: one with a password in it is refused, and the
// refusal must not hand the password on.
func New(list, key string) (*Client, error) {
	c := &Client{urls: map[string]*url.URL{}, key: key, http: &http.Client{Timeout: timeout}}
	for i, part := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == '\n' }) {
		part = strings.TrimSpace(part)
		api, raw, found := strings.Cut(part, "=")
		api = strings.TrimSpace(api)
		where := fmt.Sprintf("BREVIS_ENDPOINTS_URLS, entry %d", i+1)
		if !found || api == "" {
			return nil, fmt.Errorf("%s: not api=url", where)
		}
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s: the address is not an http(s) URL", where)
		}
		if u.User != nil {
			return nil, fmt.Errorf("%s: the address carries a credential; the key goes in BREVIS_ENDPOINTS_KEY", where)
		}
		if _, twice := c.urls[api]; twice {
			return nil, fmt.Errorf("%s: %s is listed twice", where, api)
		}
		c.urls[api] = u
	}
	return c, nil
}

// Configured reports whether the console can run this API's endpoints.
func (c *Client) Configured(api string) bool {
	if c == nil {
		return false
	}
	_, ok := c.urls[api]
	return ok
}

// Answer is one call's result, as the page draws it.
type Answer struct {
	Status    int
	Cache     string
	Millis    int64
	Columns   []string
	Rows      [][]any
	Truncated bool
	// Error is the API's own words for a refusal -- chosen there to name a
	// parameter and never quote SQL or values -- which the page repeats.
	Error string
}

// Call runs an endpoint with the parameters as given. The API validates
// them; the console forwards and does not second-guess.
func (c *Client) Call(ctx context.Context, api, path string, params url.Values) (Answer, error) {
	base, ok := c.urls[api]
	if !ok {
		return Answer{}, ErrNotConfigured
	}
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + path
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Answer{}, errors.New("the call could not be built")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		// NOT err.Error(): it carries the URL.
		return Answer{}, errors.New("the API could not be reached")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Answer{}, errors.New("the API's answer could not be read")
	}
	if len(body) > maxBody {
		return Answer{}, fmt.Errorf("the API answered more than the console shows (%d MiB); call it directly", maxBody>>20)
	}
	a := Answer{Status: resp.StatusCode, Cache: resp.Header.Get("X-Brevis-Cache"), Millis: time.Since(started).Milliseconds()}
	if resp.StatusCode != http.StatusOK {
		var refusal struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &refusal) == nil {
			a.Error = refusal.Error
		}
		if a.Error == "" {
			a.Error = http.StatusText(resp.StatusCode)
		}
		return a, nil
	}
	if err := a.read(body); err != nil {
		return Answer{}, errors.New("the API answered something that is not an endpoint's answer")
	}
	return a, nil
}

// read takes the rows IN THE ORDER THE QUERY NAMED THEIR COLUMNS. The API
// writes them that way on purpose, and a map would sort them -- so each row
// is read token by token.
func (a *Answer) read(body []byte) error {
	var top struct {
		Data      []json.RawMessage `json:"data"`
		Truncated bool              `json:"truncated"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return err
	}
	a.Truncated = top.Truncated
	index := map[string]int{}
	for _, raw := range top.Data {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return errors.New("a row is not an object")
		}
		row := make([]any, len(a.Columns))
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := t.(string)
			var v any
			if err := dec.Decode(&v); err != nil {
				return err
			}
			i, seen := index[key]
			if !seen {
				i = len(a.Columns)
				index[key] = i
				a.Columns = append(a.Columns, key)
				row = append(row, nil)
			}
			if n, isNum := v.(json.Number); isNum {
				v = n.String()
			}
			row[i] = v
		}
		a.Rows = append(a.Rows, row)
	}
	// Rows read before a column first appeared are shorter; pad them.
	for i := range a.Rows {
		for len(a.Rows[i]) < len(a.Columns) {
			a.Rows[i] = append(a.Rows[i], nil)
		}
	}
	return nil
}
