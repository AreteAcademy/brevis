// Package generation is what `brevis-sql build` writes when a model is
// rebuilt, and what `brevis-sql-api` reads to know its cached answers are
// stale -- through a Redis client of its own, written against RESP2.
//
// A CLIENT IN THIS FILE AND NOT A LIBRARY, for the reason the BigQuery REST
// endpoint is two hundred lines in this module rather than the official
// client: `brevis-sql` runs as a step in every pipeline that builds a model,
// its weight is guarded by `sql-weight.sh`, and what it asks of Redis is SET.
// What the API asks is GET, MGET and SET with an expiry. Five commands, one
// protocol, specified and short; the cost is this file and its fuzz test.
//
// WHAT IT DOES NOT DO: Sentinel, Cluster redirection, pipelining, pub/sub.
// Cluster works only because every key this module writes carries the
// `{brevis}` hash tag, which puts an MGET's keys on one slot.
package generation

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxBulk bounds one value read back. A cached answer is at most a few
	// MiB compressed; a server announcing more is wrong or hostile, and the
	// allocation is refused before it is made.
	maxBulk = 64 << 20
	// maxElems bounds an array: an MGET this module sends has one key per
	// model an endpoint reads.
	maxElems = 1 << 12
	// maxDepth bounds nesting; no reply this client asks for nests at all.
	maxDepth = 4
	// maxLine bounds a status or error line.
	maxLine = 64 << 10

	defaultTimeout = 2 * time.Second
	maxIdle        = 16
)

// Client is a small pool of connections to one Redis.
type Client struct {
	addr     string
	username string
	password string
	db       int
	tls      *tls.Config

	mu   sync.Mutex
	idle []*conn
}

type conn struct {
	nc net.Conn
	r  *bufio.Reader
}

// Open reads an address -- `host:port`, `redis://[user:password@]host:port[/db]`
// or `rediss://` for TLS -- and connects to nothing yet.
//
// AN ERROR NEVER QUOTES THE ADDRESS, which may carry a password.
func Open(addr string) (*Client, error) {
	if addr == "" {
		return nil, errors.New("the Redis address is empty")
	}
	if !strings.Contains(addr, "://") {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, errors.New("the Redis address is not host:port, redis:// or rediss://")
		}
		return &Client{addr: addr}, nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, errors.New("the Redis address is not a URL this can read")
	}
	c := &Client{}
	switch u.Scheme {
	case "redis":
	case "rediss":
		c.tls = &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
	default:
		return nil, fmt.Errorf("the Redis address has the scheme %q; redis:// or rediss://", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("the Redis address names no host")
	}
	c.addr = u.Host
	if u.Port() == "" {
		c.addr = net.JoinHostPort(u.Hostname(), "6379")
	}
	if u.User != nil {
		c.username = u.User.Username()
		c.password, _ = u.User.Password()
	}
	if db := strings.Trim(u.Path, "/"); db != "" {
		if c.db, err = strconv.Atoi(db); err != nil || c.db < 0 {
			return nil, errors.New("the Redis address's path is not a database number")
		}
	}
	return c, nil
}

// Close drops every idle connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cn := range c.idle {
		_ = cn.nc.Close()
	}
	c.idle = nil
	return nil
}

// Set stores a value. A zero ttl is no expiry, which is what a generation
// is: it stays until the next build replaces it.
func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	args := [][]byte{[]byte("SET"), []byte(key), value}
	if ttl > 0 {
		args = append(args, []byte("PX"), []byte(strconv.FormatInt(ttl.Milliseconds(), 10)))
	}
	rep, err := c.do(ctx, args)
	if err != nil {
		return err
	}
	if rep.kind != '+' {
		return fmt.Errorf("redis answered SET with %q", rep.kind)
	}
	return nil
}

// Get reads one value, and whether there was one.
func (c *Client) Get(ctx context.Context, key string) ([]byte, bool, error) {
	rep, err := c.do(ctx, [][]byte{[]byte("GET"), []byte(key)})
	if err != nil {
		return nil, false, err
	}
	if rep.kind != '$' {
		return nil, false, fmt.Errorf("redis answered GET with %q", rep.kind)
	}
	if rep.null {
		return nil, false, nil
	}
	return rep.str, true, nil
}

// MGet reads several values in one round trip; an absent one is nil.
func (c *Client) MGet(ctx context.Context, keys ...string) ([][]byte, error) {
	args := [][]byte{[]byte("MGET")}
	for _, k := range keys {
		args = append(args, []byte(k))
	}
	rep, err := c.do(ctx, args)
	if err != nil {
		return nil, err
	}
	if rep.kind != '*' || len(rep.elems) != len(keys) {
		return nil, errors.New("redis answered MGET with something that is not one value per key")
	}
	out := make([][]byte, len(keys))
	for i, e := range rep.elems {
		if e.kind != '$' {
			return nil, errors.New("redis answered MGET with something that is not a value")
		}
		if !e.null {
			out[i] = e.str
		}
	}
	return out, nil
}

// do sends one command on a pooled connection and reads its reply.
//
// A CONNECTION THAT ERRED IS DROPPED, never returned to the pool: after a
// timeout or a short read nobody knows where in the stream it stands, and
// the next caller would read the previous caller's reply.
func (c *Client) do(ctx context.Context, args [][]byte) (reply, error) {
	cn, err := c.get(ctx)
	if err != nil {
		return reply{}, err
	}
	rep, err := cn.roundTrip(ctx, args)
	if err != nil {
		_ = cn.nc.Close()
		return reply{}, err
	}
	if rep.kind == '-' {
		// A server error leaves the stream where it should be; the
		// connection is good. The message is Redis's, never the address.
		c.put(cn)
		return reply{}, fmt.Errorf("redis refused %s: %s", args[0], firstWord(rep.str))
	}
	c.put(cn)
	return rep, nil
}

func (c *Client) get(ctx context.Context) (*conn, error) {
	c.mu.Lock()
	if n := len(c.idle); n > 0 {
		cn := c.idle[n-1]
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		return cn, nil
	}
	c.mu.Unlock()
	return c.dial(ctx)
}

func (c *Client) put(cn *conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.idle) >= maxIdle {
		_ = cn.nc.Close()
		return
	}
	c.idle = append(c.idle, cn)
}

func (c *Client) dial(ctx context.Context) (*conn, error) {
	d := net.Dialer{Timeout: defaultTimeout}
	var nc net.Conn
	var err error
	if c.tls != nil {
		nc, err = (&tls.Dialer{NetDialer: &d, Config: c.tls}).DialContext(ctx, "tcp", c.addr)
	} else {
		nc, err = d.DialContext(ctx, "tcp", c.addr)
	}
	if err != nil {
		return nil, errors.New("redis could not be reached")
	}
	cn := &conn{nc: nc, r: bufio.NewReader(nc)}
	if c.password != "" {
		auth := [][]byte{[]byte("AUTH"), []byte(c.password)}
		if c.username != "" {
			auth = [][]byte{[]byte("AUTH"), []byte(c.username), []byte(c.password)}
		}
		if err := cn.expectOK(ctx, auth); err != nil {
			_ = nc.Close()
			// NOT THE SERVER'S MESSAGE: some echo what they were sent.
			return nil, errors.New("redis refused the credential")
		}
	}
	if c.db != 0 {
		if err := cn.expectOK(ctx, [][]byte{[]byte("SELECT"), []byte(strconv.Itoa(c.db))}); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("redis refused database %d", c.db)
		}
	}
	return cn, nil
}

func (cn *conn) expectOK(ctx context.Context, args [][]byte) error {
	rep, err := cn.roundTrip(ctx, args)
	if err != nil {
		return err
	}
	if rep.kind != '+' {
		return errors.New("not OK")
	}
	return nil
}

func (cn *conn) roundTrip(ctx context.Context, args [][]byte) (reply, error) {
	deadline, set := ctx.Deadline()
	if !set {
		deadline = time.Now().Add(defaultTimeout)
	}
	_ = cn.nc.SetDeadline(deadline)
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n", len(a))
		b.Write(a)
		b.WriteString("\r\n")
	}
	if _, err := io.WriteString(cn.nc, b.String()); err != nil {
		return reply{}, errors.New("redis could not be written to")
	}
	rep, err := readReply(cn.r, 0)
	if err != nil {
		return reply{}, fmt.Errorf("redis's reply could not be read: %w", err)
	}
	return rep, nil
}

// reply is one RESP2 value.
type reply struct {
	kind  byte // + - : $ *
	str   []byte
	n     int64
	null  bool
	elems []reply
}

// readReply reads one RESP2 value, refusing anything past its limits BEFORE
// allocating for it: the length comes first in the stream, and a length is
// a promise the server may not keep.
func readReply(r *bufio.Reader, depth int) (reply, error) {
	if depth > maxDepth {
		return reply{}, errors.New("nested deeper than any reply this client asks for")
	}
	line, err := readLine(r)
	if err != nil {
		return reply{}, err
	}
	if len(line) == 0 {
		return reply{}, errors.New("an empty line")
	}
	kind, rest := line[0], line[1:]
	switch kind {
	case '+', '-':
		return reply{kind: kind, str: rest}, nil
	case ':':
		n, err := strconv.ParseInt(string(rest), 10, 64)
		if err != nil {
			return reply{}, errors.New("an integer that is not one")
		}
		return reply{kind: kind, n: n}, nil
	case '$':
		n, err := strconv.ParseInt(string(rest), 10, 64)
		if err != nil || n < -1 {
			return reply{}, errors.New("a bulk length that is not one")
		}
		if n == -1 {
			return reply{kind: kind, null: true}, nil
		}
		if n > maxBulk {
			return reply{}, fmt.Errorf("a value of %d bytes, over the %d this client holds", n, maxBulk)
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return reply{}, errors.New("a value shorter than its length")
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return reply{}, errors.New("a value longer than its length")
		}
		return reply{kind: kind, str: buf[:n]}, nil
	case '*':
		n, err := strconv.ParseInt(string(rest), 10, 64)
		if err != nil || n < -1 {
			return reply{}, errors.New("an array length that is not one")
		}
		if n == -1 {
			return reply{kind: kind, null: true}, nil
		}
		if n > maxElems {
			return reply{}, fmt.Errorf("an array of %d, over the %d this client holds", n, maxElems)
		}
		out := reply{kind: kind, elems: make([]reply, 0, n)}
		for i := int64(0); i < n; i++ {
			e, err := readReply(r, depth+1)
			if err != nil {
				return reply{}, err
			}
			out.elems = append(out.elems, e)
		}
		return out, nil
	}
	return reply{}, fmt.Errorf("a reply of a kind RESP2 does not have (%q)", kind)
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, more, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, chunk...)
		if len(line) > maxLine {
			return nil, errors.New("a line longer than any reply this client asks for")
		}
		if !more {
			return line, nil
		}
	}
}

func firstWord(b []byte) string {
	w, _, _ := strings.Cut(string(b), " ")
	return w
}
